package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

// authSchema is additive like settingsSchema: older releases ignore it.
const authSchema = `
CREATE TABLE IF NOT EXISTS worker_executor_auth (
 worker_instance TEXT NOT NULL REFERENCES workers(instance_id) ON DELETE CASCADE,
 executor TEXT NOT NULL, login INTEGER NOT NULL DEFAULT 0, login_timeout_ms INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', checked_at TEXT, expires_at TEXT, updated_at TEXT NOT NULL,
 PRIMARY KEY(worker_instance,executor));
`

const maxAuthDetailBytes = 300

// ExecutorAuthStatus is the latest auth state a worker reported for one executor.
type ExecutorAuthStatus struct {
	Worker       string        `json:"worker"`
	InstanceID   string        `json:"instance_id"`
	Executor     string        `json:"executor"`
	Login        bool          `json:"login"`
	LoginTimeout time.Duration `json:"-"`
	State        string        `json:"state"`
	Detail       string        `json:"detail,omitempty"`
	CheckedAt    *time.Time    `json:"checked_at,omitempty"`
	ExpiresAt    *time.Time    `json:"expires_at,omitempty"`
	UpdatedAt    time.Time     `json:"updated_at"`
	Online       bool          `json:"online"`
}

func validAuthState(state string) bool {
	return slices.Contains([]string{protocol.AuthConnected, protocol.AuthExpiring, protocol.AuthExpired, protocol.AuthUnknown}, state)
}

// RecordExecutorAuth replaces the auth states a worker instance reported.
func (s *Store) RecordExecutorAuth(ctx context.Context, request protocol.AuthSyncRequest) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	// Register the instance without touching last_seen_at: only run polls and
	// heartbeats decide whether a worker is available for work.
	if _, err := tx.ExecContext(ctx, `INSERT INTO workers(instance_id,name,last_seen_at) VALUES(?,?,?) ON CONFLICT(instance_id) DO UPDATE SET name=excluded.name`, request.InstanceID, request.Name, now); err != nil {
		return fmt.Errorf("register worker: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM worker_executor_auth WHERE worker_instance=?`, request.InstanceID); err != nil {
		return fmt.Errorf("clear executor auth: %w", err)
	}
	for _, executor := range slices.Sorted(maps.Keys(request.Executors)) {
		report := request.Executors[executor]
		state := report.State
		if !validAuthState(state) {
			state = protocol.AuthUnknown
		}
		detail := report.Detail
		if len(detail) > maxAuthDetailBytes {
			detail = detail[:maxAuthDetailBytes]
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO worker_executor_auth(worker_instance,executor,login,login_timeout_ms,state,detail,checked_at,expires_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			request.InstanceID, executor, report.Login, max(report.LoginTimeout, 0), state, detail, optionalTime(report.CheckedAt), optionalTime(report.ExpiresAt), now); err != nil {
			return fmt.Errorf("store executor auth: %w", err)
		}
	}
	return tx.Commit()
}

// ExecutorAuthStatuses lists the latest auth state per worker name and
// executor. Online is true when the worker reported within onlineAfter.
func (s *Store) ExecutorAuthStatuses(ctx context.Context, onlineAfter time.Time) ([]ExecutorAuthStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT w.name,a.worker_instance,a.executor,a.login,a.login_timeout_ms,a.state,a.detail,COALESCE(a.checked_at,''),COALESCE(a.expires_at,''),a.updated_at
FROM worker_executor_auth a JOIN workers w ON w.instance_id=a.worker_instance ORDER BY w.name,a.executor,a.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	statuses := []ExecutorAuthStatus{}
	seen := map[string]bool{}
	for rows.Next() {
		var status ExecutorAuthStatus
		var timeoutMillis int64
		var checkedAt, expiresAt, updatedAt string
		if err := rows.Scan(&status.Worker, &status.InstanceID, &status.Executor, &status.Login, &timeoutMillis, &status.State, &status.Detail, &checkedAt, &expiresAt, &updatedAt); err != nil {
			return nil, err
		}
		key := status.Worker + "\x00" + status.Executor
		if seen[key] {
			continue
		}
		seen[key] = true
		status.LoginTimeout = time.Duration(timeoutMillis) * time.Millisecond
		status.CheckedAt, status.ExpiresAt = parseOptionalTime(checkedAt), parseOptionalTime(expiresAt)
		if updated := parseOptionalTime(updatedAt); updated != nil {
			status.UpdatedAt = *updated
			status.Online = !updated.Before(onlineAfter)
		}
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

// expiredExecutors lists the executors whose auth a worker instance reported
// as expired. Runs for them are not leased to that instance.
func expiredExecutors(ctx context.Context, tx *sql.Tx, instanceID string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT executor FROM worker_executor_auth WHERE worker_instance=? AND state=?`, instanceID, protocol.AuthExpired)
	if err != nil {
		return nil, fmt.Errorf("read executor auth: %w", err)
	}
	defer rows.Close()
	expired := map[string]bool{}
	for rows.Next() {
		var executor string
		if err := rows.Scan(&executor); err != nil {
			return nil, err
		}
		expired[executor] = true
	}
	return expired, rows.Err()
}

func optionalTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}
