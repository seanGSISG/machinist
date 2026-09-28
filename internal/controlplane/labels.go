package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

var labelKeyPattern = regexp.MustCompile(`^[a-z0-9_.-]+$`)

func validateLabels(labels map[string]string) error {
	for key, value := range labels {
		if len(key) > 64 || !labelKeyPattern.MatchString(key) {
			return fmt.Errorf("invalid label key %q", key)
		}
		if utf8.RuneCountInString(value) > 256 {
			return fmt.Errorf("label %q value must be at most 256 characters", key)
		}
	}
	return nil
}

func (s *Store) createLabeledJob(ctx context.Context, prompt, repository, name string, command config.ResolvedCommand, labels map[string]string, supersedes string) (string, int, error) {
	if command.Name == "" {
		return "", 0, errors.New("job must contain one command")
	}
	return s.createLabeledJobTx(ctx, prompt, repository, name, labels, supersedes, func(tx *sql.Tx, jobID, now string) error {
		runID, err := randomID("run", 12)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO runs(id,job_id,command,command_hash,executor,model,repository,rendered_prompt,timeout_ms,state) VALUES(?,?,?,?,?,?,?,?,?,'queued')`, runID, jobID, command.Name, command.Hash, command.Executor, command.Model, repository, command.Prompt, command.Timeout.Milliseconds())
		return err
	})
}

func (s *Store) createLabeledTaskJob(ctx context.Context, task protocol.Task, repository, name string, steps []config.WorkflowStep, labels map[string]string, supersedes string) (string, int, error) {
	if len(steps) == 0 {
		return "", 0, errors.New("workflow requires steps")
	}
	plan, err := json.Marshal(steps)
	if err != nil {
		return "", 0, err
	}
	return s.createLabeledJobTx(ctx, task.Brief(), repository, name, labels, supersedes, func(tx *sql.Tx, jobID, now string) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_jobs(job_id,name,plan) VALUES(?,?,?)`, jobID, name, string(plan)); err != nil {
			return err
		}
		raw, err := json.Marshal(task)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_inputs(job_id,task) VALUES(?,?)`, jobID, string(raw)); err != nil {
			return err
		}
		return enqueueStep(ctx, tx, jobID, repository, 0, steps[0], false, now)
	})
}

// createLabeledJobTx keeps admission metadata and replacement in the same
// write transaction. OpenStore serializes transactions onto its single SQLite
// connection, so the first insert acquires the SQLite write lock before any
// superseded state is inspected.
func (s *Store) createLabeledJobTx(ctx context.Context, prompt, repository, name string, labels map[string]string, supersedes string, insertRelated func(*sql.Tx, string, string) error) (string, int, error) {
	jobID, err := randomID("job", 12)
	if err != nil {
		return "", 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,prompt,repository,command,supersedes_job_id,state,created_at,updated_at) VALUES(?,?,?,?,?,'queued',?,?)`, jobID, prompt, repository, name, nullableText(supersedes), now, now); err != nil {
		return "", 0, fmt.Errorf("insert job: %w", err)
	}
	if err := insertRelated(tx, jobID, now); err != nil {
		return "", 0, err
	}
	for key, value := range labels {
		if _, err := tx.ExecContext(ctx, `INSERT INTO job_labels(job_id,label_key,value) VALUES(?,?,?)`, jobID, key, value); err != nil {
			return "", 0, fmt.Errorf("insert job label: %w", err)
		}
	}
	if supersedes != "" {
		status, err := s.cancelJobTx(ctx, tx, supersedes, "supersede")
		if err != nil {
			return "", 0, err
		}
		if status == http.StatusNotFound || status == http.StatusConflict {
			return "", status, nil
		}
	}
	if err := tx.Commit(); err != nil {
		return "", 0, fmt.Errorf("commit job: %w", err)
	}
	return jobID, http.StatusCreated, nil
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) enrichJobSummaries(ctx context.Context, summaries []JobSummary) error {
	byID := make(map[string]*JobSummary, len(summaries))
	for i := range summaries {
		if summaries[i].Labels == nil {
			summaries[i].Labels = map[string]string{}
		}
		byID[summaries[i].ID] = &summaries[i]
	}
	rows, err := s.db.QueryContext(ctx, `SELECT job_id,label_key,value FROM job_labels`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, key, value string
		if err := rows.Scan(&id, &key, &value); err != nil {
			rows.Close()
			return err
		}
		if summary := byID[id]; summary != nil {
			summary.Labels[key] = value
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT DISTINCT supersedes_job_id FROM jobs WHERE supersedes_job_id IS NOT NULL AND supersedes_job_id<>''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if summary := byID[id]; summary != nil {
			summary.Superseded = true
		}
	}
	return rows.Err()
}
