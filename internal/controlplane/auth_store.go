package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"strings"
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

// RecordExecutorAuth replaces the auth states a worker instance reported.
func (s *Store) RecordExecutorAuth(ctx context.Context, request protocol.AuthSyncRequest) error {
	_, err := s.recordExecutorAuth(ctx, request)
	return err
}

func (s *Store) recordExecutorAuth(ctx context.Context, request protocol.AuthSyncRequest) (map[string]time.Time, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	nowTime := s.now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	// Register the instance without touching last_seen_at: only run polls and
	// heartbeats decide whether a worker is available for work.
	if _, err := tx.ExecContext(ctx, `INSERT INTO workers(instance_id,name,last_seen_at) VALUES(?,?,?) ON CONFLICT(instance_id) DO UPDATE SET name=excluded.name`, request.InstanceID, request.Name, now); err != nil {
		return nil, fmt.Errorf("register worker: %w", err)
	}
	previous, err := previousExecutorAuth(ctx, tx, request.InstanceID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM worker_executor_auth WHERE worker_instance=?`, request.InstanceID); err != nil {
		return nil, fmt.Errorf("clear executor auth: %w", err)
	}
	recheckAt := map[string]time.Time{}
	for _, executor := range slices.Sorted(maps.Keys(request.Executors)) {
		report := request.Executors[executor]
		prior, existed := previous[executor]
		priorState := prior.state
		fsm := authFSMForStoredState(priorState)
		before := authEventState(priorState, existed)
		after := before
		state := stateWithoutObservation(priorState, report.State, existed)
		observation := isAuthObservation(report)
		if observation && !sameAuthCheck(prior.checkedAt, report.CheckedAt) {
			result := authCheckResult(report)
			// A worker's first expired observation is authoritative. Hysteresis is
			// useful only when there is a previously healthy login to preserve.
			if priorState == "" && report.State == protocol.AuthExpired {
				result.LoggedOut = true
			}
			var deadline time.Time
			after, deadline = fsm.Observe(result, nowTime)
			if !deadline.IsZero() {
				recheckAt[executor] = deadline
			}
			state = storedAuthState(after, report.State)
		} else if observation {
			state = priorState
		} else {
			after = authEventState(state, true)
		}
		if before != after {
			if err := s.AppendEvent(ctx, tx, Event{
				Type: "auth_change", SubjectKind: "executor", SubjectID: request.Name + "/" + executor,
				Cause: "auth_check", Payload: map[string]string{"from": before, "to": after}, CreatedAt: nowTime,
			}); err != nil {
				return nil, err
			}
		}
		detail := truncateUTF8(report.Detail, maxAuthDetailBytes)
		if _, err := tx.ExecContext(ctx, `INSERT INTO worker_executor_auth(worker_instance,executor,login,login_timeout_ms,state,detail,checked_at,expires_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			request.InstanceID, executor, report.Login, max(report.LoginTimeout, 0), state, detail, optionalTime(report.CheckedAt), optionalTime(report.ExpiresAt), now); err != nil {
			return nil, fmt.Errorf("store executor auth: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return recheckAt, nil
}

type storedExecutorAuth struct {
	state     string
	checkedAt *time.Time
}

func previousExecutorAuth(ctx context.Context, tx *sql.Tx, instanceID string) (map[string]storedExecutorAuth, error) {
	rows, err := tx.QueryContext(ctx, `SELECT executor,state,COALESCE(checked_at,'') FROM worker_executor_auth WHERE worker_instance=?`, instanceID)
	if err != nil {
		return nil, fmt.Errorf("read previous executor auth: %w", err)
	}
	defer rows.Close()
	states := map[string]storedExecutorAuth{}
	for rows.Next() {
		var executor, state, checkedAt string
		if err := rows.Scan(&executor, &state, &checkedAt); err != nil {
			return nil, err
		}
		states[executor] = storedExecutorAuth{state: state, checkedAt: parseOptionalTime(checkedAt)}
	}
	return states, rows.Err()
}

func sameAuthCheck(previous, current *time.Time) bool {
	return previous != nil && current != nil && previous.Equal(*current)
}

func authFSMForStoredState(state string) authFSM {
	switch state {
	case protocol.AuthExpired:
		return authFSM{state: authStateExpired, softFailures: 2}
	case authStateRechecking:
		return authFSM{state: authStateRechecking, softFailures: 1}
	default:
		return authFSM{state: authStateOK}
	}
}

// isAuthObservation excludes snapshots for recipes with no status command and
// checks where the worker could not determine auth state. Neither is evidence
// that a previously working login has expired.
func isAuthObservation(report protocol.ExecutorAuthReport) bool {
	return report.CheckedAt != nil && report.State != protocol.AuthUnknown
}

func stateWithoutObservation(prior, reported string, existed bool) string {
	if existed && reported == protocol.AuthUnknown {
		return prior
	}
	return reported
}

func authEventState(stored string, existed bool) string {
	if !existed || stored == protocol.AuthUnknown {
		return protocol.AuthUnknown
	}
	switch stored {
	case protocol.AuthConnected, protocol.AuthExpiring:
		return authStateOK
	default:
		return stored
	}
}

func authCheckResult(report protocol.ExecutorAuthReport) CheckResult {
	result := CheckResult{ExpiresAt: report.ExpiresAt}
	switch report.State {
	case protocol.AuthConnected, protocol.AuthExpiring:
		result.Success = true
	}
	detail := strings.ToLower(report.Detail)
	if strings.Contains(detail, "401") {
		result.StatusCode = 401
	}
	result.LoggedOut = strings.Contains(detail, "logged out") || strings.Contains(detail, "logout") ||
		strings.Contains(detail, "not logged in") || detail == "status check did not report a login"
	return result
}

func storedAuthState(state, reported string) string {
	switch state {
	case authStateOK:
		if reported == protocol.AuthExpiring {
			return protocol.AuthExpiring
		}
		return protocol.AuthConnected
	case authStateExpired:
		return protocol.AuthExpired
	default:
		return authStateRechecking
	}
}

// ExecutorAuthStatuses lists the latest auth state per worker name and
// executor: with several instances of one name (a restart), the instance that
// reported last wins. Online is true when the worker reported within onlineAfter.
func (s *Store) ExecutorAuthStatuses(ctx context.Context, onlineAfter time.Time) ([]ExecutorAuthStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT w.name,a.worker_instance,a.executor,a.login,a.login_timeout_ms,a.state,a.detail,COALESCE(a.checked_at,''),COALESCE(a.expires_at,''),a.updated_at
FROM worker_executor_auth a JOIN workers w ON w.instance_id=a.worker_instance ORDER BY w.name,a.executor,julianday(a.updated_at) DESC,a.worker_instance DESC`)
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
