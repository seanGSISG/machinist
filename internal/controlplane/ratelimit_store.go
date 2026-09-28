package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

const (
	minimumRateLimitBackoff = time.Minute
	maximumRateLimitBackoff = time.Hour
)

// parkRateLimitedExecutor records the stall and returns the same run to the
// queue. Reusing the run preserves the workflow attempt: rate limits are an
// infrastructure retry, not an outcome of the attempt. The rate-limit fields
// remain on the run so clients can explain why it was requeued; other terminal
// completion fields are cleared before it can be leased again.
func (s *Store) parkRateLimitedExecutor(ctx context.Context, tx *sql.Tx, runID, jobID, worker, executor string, completion protocol.Completion, now time.Time) error {
	until, backoff, err := rateLimitReset(ctx, tx, worker, executor, completion.ResetAt, now)
	if err != nil {
		return err
	}
	formattedNow := now.UTC().Format(time.RFC3339Nano)
	formattedUntil := until.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO executor_state(worker,executor,unavailable_until,backoff_seconds,reset_source,updated_at) VALUES(?,?,?,?,?,?)
ON CONFLICT(worker,executor) DO UPDATE SET unavailable_until=excluded.unavailable_until,backoff_seconds=excluded.backoff_seconds,reset_source=excluded.reset_source,updated_at=excluded.updated_at`,
		worker, executor, formattedUntil, int64(backoff/time.Second), nullableText(completion.ResetSource), formattedNow); err != nil {
		return fmt.Errorf("park rate-limited executor: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET state='queued',worker_instance=NULL,worker_name='',lease_token=NULL,lease_expires_at=NULL,started_at=NULL,exit_code=NULL,error=NULL,result=NULL,events=NULL,completed_at=NULL,duration_millis=NULL,token_usage=NULL WHERE id=?`, runID); err != nil {
		return fmt.Errorf("requeue rate-limited run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='queued',updated_at=? WHERE id=?`, formattedNow, jobID); err != nil {
		return fmt.Errorf("requeue rate-limited job: %w", err)
	}
	if err := s.AppendEvent(ctx, tx, Event{
		Type: "rate_limit_stall_start", SubjectKind: "executor", SubjectID: worker + "/" + executor, Cause: "completion",
		Payload: map[string]any{"worker": worker, "executor": executor, "until": formattedUntil, "source": completion.ResetSource}, CreatedAt: now,
	}); err != nil {
		return err
	}
	return nil
}

func rateLimitReset(ctx context.Context, tx *sql.Tx, worker, executor string, resetAt *time.Time, now time.Time) (time.Time, time.Duration, error) {
	if resetAt != nil && !resetAt.IsZero() {
		until := resetAt.UTC()
		backoff := until.Sub(now.UTC())
		if backoff < 0 {
			backoff = 0
		}
		return until, backoff, nil
	}
	var previous sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT backoff_seconds FROM executor_state WHERE worker=? AND executor=?`, worker, executor).Scan(&previous)
	if err != nil && err != sql.ErrNoRows {
		return time.Time{}, 0, fmt.Errorf("read executor rate-limit backoff: %w", err)
	}
	backoff := minimumRateLimitBackoff
	if previous.Valid && previous.Int64 > 0 {
		backoff = min(time.Duration(previous.Int64)*time.Second*2, maximumRateLimitBackoff)
	}
	return now.UTC().Add(backoff), backoff, nil
}

// skipRateLimitedExecutors removes executor capabilities that are still
// parked for this worker name. Expired rows remain until a lease succeeds.
func skipRateLimitedExecutors(ctx context.Context, tx *sql.Tx, worker string, executors map[string]bool, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT executor,unavailable_until FROM executor_state WHERE worker=? AND unavailable_until IS NOT NULL`, worker)
	if err != nil {
		return fmt.Errorf("read rate-limited executors: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var executor, value string
		if err := rows.Scan(&executor, &value); err != nil {
			return err
		}
		until, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return fmt.Errorf("parse rate limit for %s/%s: %w", worker, executor, err)
		}
		if until.After(now) {
			delete(executors, executor)
		}
	}
	return rows.Err()
}

// endExpiredRateLimit clears a stall only after its executor has successfully
// leased work. RowsAffected makes the end event exactly once.
func (s *Store) endExpiredRateLimit(ctx context.Context, tx *sql.Tx, worker, executor string, now time.Time) error {
	var value string
	err := tx.QueryRowContext(ctx, `SELECT unavailable_until FROM executor_state WHERE worker=? AND executor=? AND unavailable_until IS NOT NULL`, worker, executor).Scan(&value)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read expired executor rate limit: %w", err)
	}
	until, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return fmt.Errorf("parse rate limit for %s/%s: %w", worker, executor, err)
	}
	if until.After(now) {
		return nil
	}
	formattedNow := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE executor_state SET unavailable_until=NULL,backoff_seconds=0,updated_at=? WHERE worker=? AND executor=? AND unavailable_until=?`, formattedNow, worker, executor, value)
	if err != nil {
		return fmt.Errorf("clear expired executor rate limit: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return nil
	}
	return s.AppendEvent(ctx, tx, Event{
		Type: "rate_limit_stall_end", SubjectKind: "executor", SubjectID: worker + "/" + executor, Cause: "expired",
		Payload: map[string]any{"worker": worker, "executor": executor}, CreatedAt: now,
	})
}
