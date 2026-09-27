package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"
)

type cancelResponse struct {
	JobID string `json:"job_id"`
	State string `json:"state"`
}

func (s *Server) cancelJob(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	workflow, err := s.store.workflowJob(request.Context(), id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusInternalServerError, errors.New("cancel job"))
		return
	}

	var status int
	if errors.Is(err, sql.ErrNoRows) {
		status = http.StatusNotFound
		err = nil
	} else if workflow {
		status, err = s.store.cancelWorkflowJob(request.Context(), id)
	} else {
		status, err = s.store.CancelDirectJob(request.Context(), id)
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, errors.New("cancel job"))
		return
	}

	state := ""
	if status != http.StatusNotFound {
		if err := s.store.db.QueryRowContext(request.Context(), `SELECT state FROM jobs WHERE id=?`, id).Scan(&state); err != nil {
			writeError(response, http.StatusInternalServerError, errors.New("read cancelled job"))
			return
		}
	}
	writeJSON(response, status, cancelResponse{JobID: id, State: state})
}

func (s *Store) workflowJob(ctx context.Context, id string) (bool, error) {
	var workflow bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_jobs WHERE job_id=j.id) FROM jobs j WHERE j.id=?`, id).Scan(&workflow)
	return workflow, err
}

// CancelDirectJob cancels a non-workflow job. The state change, run update,
// and audit event are committed together.
func (s *Store) CancelDirectJob(ctx context.Context, id string) (int, error) {
	return s.cancelJob(ctx, id)
}

// cancelWorkflowJob follows WorkflowAction's cancel transition while keeping
// the cancellation event in the same transaction.
func (s *Store) cancelWorkflowJob(ctx context.Context, id string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	status, err := s.cancelWorkflowJobTx(ctx, tx, id, "api")
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return status, nil
}

func (s *Store) cancelJob(ctx context.Context, id string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	status, err := s.cancelJobTx(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return status, nil
}

// cancelJobTx is shared by cancellation and transactional job replacement.
// The caller owns the transaction and must commit or roll it back.
func (s *Store) cancelJobTx(ctx context.Context, tx *sql.Tx, id string, causes ...string) (int, error) {
	cause := "api"
	if len(causes) != 0 {
		cause = causes[0]
	}
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM jobs WHERE id=?`, id).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return http.StatusNotFound, nil
		}
		return 0, err
	}
	if state == "cancelled" {
		return http.StatusOK, nil
	}
	if state != "queued" && state != "running" && state != "awaiting_approval" {
		return http.StatusConflict, nil
	}

	now := s.now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET state='cancelled',exit_code=130,error='Cancelled by operator',lease_expires_at=NULL,completed_at=? WHERE job_id=? AND state IN ('queued','running','awaiting_approval')`, nowText, id); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET state='cancelled',updated_at=? WHERE id=? AND state IN ('queued','running','awaiting_approval')`, nowText, id)
	if err != nil {
		return 0, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if changed != 1 {
		return http.StatusConflict, nil
	}
	if err := s.AppendEvent(ctx, tx, Event{
		Type: "cancel", SubjectKind: "job", SubjectID: id, Cause: cause,
		Payload: cancelResponse{JobID: id, State: "cancelled"}, CreatedAt: now,
	}); err != nil {
		return 0, err
	}
	return http.StatusAccepted, nil
}

func (s *Store) cancelWorkflowJobTx(ctx context.Context, tx *sql.Tx, id, cause string) (int, error) {
	var state, latestRun string
	err := tx.QueryRowContext(ctx, `SELECT j.state,(SELECT id FROM runs WHERE job_id=j.id ORDER BY rowid DESC LIMIT 1)
		FROM jobs j JOIN workflow_jobs w ON w.job_id=j.id WHERE j.id=?`, id).Scan(&state, &latestRun)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return http.StatusNotFound, nil
		}
		return 0, err
	}
	if state == "cancelled" {
		return http.StatusOK, nil
	}
	if state == "succeeded" {
		return http.StatusConflict, nil
	}

	now := s.now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET state='cancelled',exit_code=130,error='Cancelled by operator',lease_expires_at=NULL,completed_at=? WHERE id=? AND state IN ('queued','running','awaiting_approval')`, nowText, latestRun); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='cancelled',updated_at=? WHERE id=?`, nowText, id); err != nil {
		return 0, err
	}
	if err := s.AppendEvent(ctx, tx, Event{
		Type: "cancel", SubjectKind: "job", SubjectID: id, Cause: cause,
		Payload: cancelResponse{JobID: id, State: "cancelled"}, CreatedAt: now,
	}); err != nil {
		return 0, err
	}
	return http.StatusAccepted, nil
}
