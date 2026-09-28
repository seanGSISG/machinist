package controlplane

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

const (
	defaultReadLimit = 100
	maxReadLimit     = 1000
	jobRunLimit      = 20
)

type jobsResponse struct {
	Jobs []JobSummary `json:"jobs"`
}

type jobDetailResponse struct {
	Task             *protocol.Task    `json:"task,omitempty"`
	Workflow         *WorkflowProgress `json:"workflow,omitempty"`
	ID               string            `json:"id"`
	Prompt           string            `json:"prompt"`
	Repository       string            `json:"repository"`
	GitHubIssueTitle string            `json:"github_issue_title,omitempty"`
	Command          string            `json:"command"`
	TriggerID        string            `json:"trigger_id,omitempty"`
	OccurrenceKey    string            `json:"occurrence_key,omitempty"`
	TriggerSubject   string            `json:"trigger_subject,omitempty"`
	State            string            `json:"state"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	Runs             []runSummary      `json:"runs"`
}

type runSummary struct {
	Run
	FailureClass *string    `json:"failure_class"`
	ResetAt      *time.Time `json:"reset_at"`
	ResetSource  *string    `json:"reset_source"`
}

type runDetailResponse struct {
	Run
	JobID          string          `json:"job_id"`
	Result         json.RawMessage `json:"result"`
	Events         string          `json:"events"`
	RenderedPrompt string          `json:"rendered_prompt"`
	FailureClass   *string         `json:"failure_class"`
	ResetAt        *time.Time      `json:"reset_at"`
	ResetSource    *string         `json:"reset_source"`
}

type eventResponse struct {
	ID          int64           `json:"id"`
	Type        string          `json:"type"`
	SubjectKind string          `json:"subject_kind"`
	SubjectID   string          `json:"subject_id"`
	Cause       string          `json:"cause"`
	Payload     json.RawMessage `json:"payload"`
	CreatedAt   time.Time       `json:"created_at"`
}

func (s *Server) listJobs(response http.ResponseWriter, request *http.Request) {
	limit, err := readLimit(request, defaultReadLimit)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	state := strings.TrimSpace(request.URL.Query().Get("state"))
	labelKey, labelValue, err := parseLabelFilter(request.URL.Query().Get("label"))
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	jobs, err := s.store.listJobs(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	var labeled map[string]bool
	if labelKey != "" {
		labeled, err = s.jobsWithLabel(request, labelKey, labelValue)
		if err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
	}
	summaries := make([]JobSummary, 0, min(limit, len(jobs)))
	for _, job := range jobs {
		if state != "" && job.State != state || labeled != nil && !labeled[job.ID] {
			continue
		}
		summaries = append(summaries, newJobSummary(job))
		if len(summaries) == limit {
			break
		}
	}
	if err := s.store.enrichJobSummaries(request.Context(), summaries); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, jobsResponse{Jobs: summaries})
}

func (s *Server) jobsWithLabel(request *http.Request, key, value string) (map[string]bool, error) {
	rows, err := s.store.db.QueryContext(request.Context(), `SELECT job_id FROM job_labels WHERE label_key=? AND value=?`, key, value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		jobs[id] = true
	}
	return jobs, rows.Err()
}

func (s *Server) jobDetail(response http.ResponseWriter, request *http.Request) {
	jobs, err := s.store.listJobs(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	for _, job := range jobs {
		if job.ID != request.PathValue("id") {
			continue
		}
		start := max(0, len(job.Runs)-jobRunLimit)
		runs := make([]runSummary, 0, len(job.Runs)-start)
		for _, run := range job.Runs[start:] {
			detail, err := s.runFailureFields(request, run)
			if err != nil {
				writeError(response, http.StatusInternalServerError, err)
				return
			}
			runs = append(runs, detail)
		}
		writeJSON(response, http.StatusOK, jobDetailResponse{
			Task: job.Task, Workflow: job.Workflow, ID: job.ID, Prompt: job.Prompt,
			Repository: job.Repository, GitHubIssueTitle: job.GitHubIssueTitle,
			Command: job.Command, TriggerID: job.TriggerID, OccurrenceKey: job.OccurrenceKey,
			TriggerSubject: job.TriggerSubject, State: job.State, CreatedAt: job.CreatedAt,
			UpdatedAt: job.UpdatedAt, Runs: runs,
		})
		return
	}
	writeError(response, http.StatusNotFound, sql.ErrNoRows)
}

func (s *Server) runFailureFields(request *http.Request, run Run) (runSummary, error) {
	var failureClass, resetAt, resetSource sql.NullString
	err := s.store.db.QueryRowContext(request.Context(), `SELECT failure_class,reset_at,reset_source FROM runs WHERE id=?`, run.ID).Scan(&failureClass, &resetAt, &resetSource)
	if err != nil {
		return runSummary{}, err
	}
	return runSummary{Run: run, FailureClass: nullableString(failureClass), ResetAt: nullableTime(resetAt), ResetSource: nullableString(resetSource)}, nil
}

func (s *Server) runDetail(response http.ResponseWriter, request *http.Request) {
	var detail runDetailResponse
	var result, events string
	var failureClass, resetAt, resetSource sql.NullString
	err := s.store.db.QueryRowContext(request.Context(), `SELECT job_id,COALESCE(result,''),COALESCE(events,''),rendered_prompt,failure_class,reset_at,reset_source FROM runs WHERE id=?`, request.PathValue("id")).Scan(
		&detail.JobID, &result, &events, &detail.RenderedPrompt,
		&failureClass, &resetAt, &resetSource)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	jobs, err := s.store.listJobs(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	for _, job := range jobs {
		for _, run := range job.Runs {
			if run.ID == request.PathValue("id") {
				detail.Run = run
				break
			}
		}
	}
	detail.Result = json.RawMessage("null")
	if json.Valid([]byte(result)) {
		detail.Result = json.RawMessage(result)
	}
	detail.Events = events
	detail.FailureClass = nullableString(failureClass)
	detail.ResetAt = nullableTime(resetAt)
	detail.ResetSource = nullableString(resetSource)
	writeJSON(response, http.StatusOK, detail)
}

func (s *Server) listEvents(response http.ResponseWriter, request *http.Request) {
	limit, err := readLimit(request, defaultReadLimit)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	var since time.Time
	if raw := strings.TrimSpace(request.URL.Query().Get("since")); raw != "" {
		since, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(response, http.StatusBadRequest, errors.New("since must be RFC3339"))
			return
		}
	}
	eventType := strings.TrimSpace(request.URL.Query().Get("type"))
	rows, err := s.store.db.QueryContext(request.Context(), `SELECT id,type,subject_kind,subject_id,cause,payload_json,created_at FROM events WHERE (?='' OR type=?) AND (?='' OR created_at>=?) ORDER BY created_at DESC,id DESC LIMIT ?`, eventType, eventType, sinceString(since), sinceString(since), limit)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	events := []eventResponse{}
	for rows.Next() {
		var event eventResponse
		var payload, created string
		if err := rows.Scan(&event.ID, &event.Type, &event.SubjectKind, &event.SubjectID, &event.Cause, &payload, &created); err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
		event.Payload = json.RawMessage(payload)
		event.CreatedAt = parseTime(created)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Events []eventResponse `json:"events"`
	}{Events: events})
}

func readLimit(request *http.Request, defaultLimit int) (int, error) {
	raw := strings.TrimSpace(request.URL.Query().Get("limit"))
	if raw == "" {
		return defaultLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > maxReadLimit {
		return 0, fmt.Errorf("limit must be between 1 and %d", maxReadLimit)
	}
	return limit, nil
}

func parseLabelFilter(raw string) (string, string, error) {
	if raw == "" {
		return "", "", nil
	}
	key, value, ok := strings.Cut(raw, ":")
	if !ok || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
		return "", "", errors.New("label must have the form key:value")
	}
	return strings.TrimSpace(key), strings.TrimSpace(value), nil
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullableTime(value sql.NullString) *time.Time {
	if !value.Valid {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &parsed
}

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func sinceString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
