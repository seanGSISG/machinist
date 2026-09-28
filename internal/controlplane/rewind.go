package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

type rewindResponse struct {
	JobID   string `json:"job_id"`
	Step    int    `json:"step"`
	Attempt int    `json:"attempt"`
}

var rewindReasons = map[string]bool{"rewind": true, "loop": true, "retry": true}

func (s *Server) rewindJob(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	var input struct {
		Step   json.RawMessage `json:"step"`
		Reason string          `json:"reason"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if !rewindReasons[input.Reason] {
		writeError(response, http.StatusBadRequest, errors.New("reason must be rewind, loop or retry"))
		return
	}
	status, result, err := s.store.RewindWorkflow(request.Context(), request.PathValue("id"), input.Step, input.Reason)
	if err != nil {
		writeError(response, http.StatusInternalServerError, errors.New("rewind job"))
		return
	}
	switch status {
	case http.StatusAccepted:
		writeJSON(response, status, result)
	case http.StatusNotFound:
		writeError(response, status, errors.New("job not found"))
	case http.StatusBadRequest:
		writeError(response, status, errors.New("unknown workflow step"))
	default:
		writeError(response, status, errors.New("job cannot be rewound to that step"))
	}
}

// RewindWorkflow appends a new attempt of an earlier workflow step. Finished
// run and attempt rows are left untouched; an in-flight later step is
// cancelled through the shared cancel helper in the same transaction.
func (s *Store) RewindWorkflow(ctx context.Context, job string, step json.RawMessage, reason string) (int, rewindResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, rewindResponse{}, err
	}
	defer tx.Rollback()
	// database/sql cannot issue BEGIN IMMEDIATE per transaction. A write
	// statement, even one matching no rows, takes the reserved lock before any
	// read, which gives the same guarantee.
	if _, err = tx.ExecContext(ctx, `DELETE FROM workflow_loop_counters WHERE 0`); err != nil {
		return 0, rewindResponse{}, err
	}

	var state, repository string
	var plan sql.NullString
	var current sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT j.state,j.repository,w.plan,w.current_step FROM jobs j LEFT JOIN workflow_jobs w ON w.job_id=j.id WHERE j.id=?`, job).Scan(&state, &repository, &plan, &current)
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound, rewindResponse{}, nil
	}
	if err != nil {
		return 0, rewindResponse{}, err
	}
	if !plan.Valid {
		return http.StatusConflict, rewindResponse{}, nil
	}
	var steps []config.WorkflowStep
	if err = json.Unmarshal([]byte(plan.String), &steps); err != nil {
		return 0, rewindResponse{}, err
	}
	index, ok := resolveWorkflowStep(steps, step)
	if !ok {
		return http.StatusBadRequest, rewindResponse{}, nil
	}
	active := state == "queued" || state == "running" || state == "awaiting_approval"
	if index > int(current.Int64) || (active && index == int(current.Int64)) {
		return http.StatusConflict, rewindResponse{}, nil
	}

	var previousRun string
	var attempts, highest int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(a.attempt),0),COALESCE((SELECT r2.id FROM runs r2 JOIN workflow_attempts a2 ON a2.run_id=r2.id WHERE r2.job_id=? AND a2.step=? ORDER BY r2.rowid DESC LIMIT 1),'')
		FROM runs r JOIN workflow_attempts a ON a.run_id=r.id WHERE r.job_id=? AND a.step=?`, job, index, job, index).Scan(&attempts, &highest, &previousRun); err != nil {
		return 0, rewindResponse{}, err
	}
	// Attempts created before migration 7 have no number; count them too.
	attempt := max(attempts, highest) + 1

	if active {
		if _, err = s.cancelWorkflowJobTx(ctx, tx, job, "rewind"); err != nil {
			return 0, rewindResponse{}, err
		}
	}
	now := s.now().UTC()
	if err = enqueueStep(ctx, tx, job, repository, index, steps[index], false, now.Format(time.RFC3339Nano)); err != nil {
		return 0, rewindResponse{}, err
	}
	// Only the attempt row inserted above gains its lineage.
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_attempts SET attempt=?,previous_run_id=NULLIF(?,''),reason=? WHERE run_id=(SELECT id FROM runs WHERE job_id=? ORDER BY rowid DESC LIMIT 1)`, attempt, previousRun, reason, job); err != nil {
		return 0, rewindResponse{}, err
	}
	result := rewindResponse{JobID: job, Step: index, Attempt: attempt}
	if err = s.AppendEvent(ctx, tx, Event{
		Type: "rewind", SubjectKind: "job", SubjectID: job, Cause: reason, Payload: result, CreatedAt: now,
	}); err != nil {
		return 0, rewindResponse{}, err
	}
	if err = tx.Commit(); err != nil {
		return 0, rewindResponse{}, err
	}
	return http.StatusAccepted, result, nil
}

// resolveWorkflowStep accepts a zero-based step index or a step id or command
// name.
func resolveWorkflowStep(steps []config.WorkflowStep, raw json.RawMessage) (int, bool) {
	var index int
	if err := json.Unmarshal(raw, &index); err == nil {
		return index, index >= 0 && index < len(steps)
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil || name == "" {
		return 0, false
	}
	for i, step := range steps {
		if step.ID == name {
			return i, true
		}
	}
	for i, step := range steps {
		if step.Command.Name == name {
			return i, true
		}
	}
	return 0, false
}
