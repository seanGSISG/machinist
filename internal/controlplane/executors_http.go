package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	executorStateOK          = "ok"
	executorStateRateLimited = "rate_limited"
	executorReasonAvailable  = "Available"
	executorReasonLimited    = "RateLimited"
)

type clearRateLimitResponse struct {
	Worker   string `json:"worker"`
	Executor string `json:"executor"`
	State    string `json:"state"`
}

func (s *Server) clearRateLimit(response http.ResponseWriter, request *http.Request) {
	worker, executor := request.PathValue("worker"), request.PathValue("executor")
	found, err := s.store.clearExecutorRateLimit(request.Context(), worker, executor)
	if err != nil {
		writeError(response, http.StatusInternalServerError, errors.New("clear executor rate limit"))
		return
	}
	if !found {
		writeError(response, http.StatusNotFound, errors.New("executor not found"))
		return
	}
	writeJSON(response, http.StatusOK, clearRateLimitResponse{Worker: worker, Executor: executor, State: executorStateOK})
}

// clearExecutorRateLimit ends a stall early on an operator's request. It
// reports false when neither a worker advertises the executor nor a state row
// exists; clearing an executor that is not limited is a no-op.
func (s *Store) clearExecutorRateLimit(ctx context.Context, worker, executor string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var known bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worker_executors e JOIN workers w ON w.instance_id=e.worker_instance WHERE w.name=? AND e.executor=?)
OR EXISTS(SELECT 1 FROM executor_state WHERE worker=? AND executor=?)`, worker, executor, worker, executor).Scan(&known); err != nil {
		return false, fmt.Errorf("find executor: %w", err)
	}
	if !known {
		return false, nil
	}
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE executor_state SET unavailable_until=NULL,backoff_seconds=0,updated_at=? WHERE worker=? AND executor=? AND unavailable_until IS NOT NULL`,
		now.Format(time.RFC3339Nano), worker, executor)
	if err != nil {
		return false, fmt.Errorf("clear executor rate limit: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed > 0 {
		if err := s.AppendEvent(ctx, tx, Event{
			Type: "rate_limit_stall_end", SubjectKind: "executor", SubjectID: worker + "/" + executor, Cause: "operator",
			Payload: map[string]any{"worker": worker, "executor": executor}, CreatedAt: now,
		}); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// ExecutorStatuses lists every executor a registered worker advertises plus
// any executor with stored rate-limit state, ordered by worker then executor.
// A limit whose reset time has passed reads as ok even before the next lease
// clears the row.
func (s *Store) ExecutorStatuses(ctx context.Context) ([]ExecutorStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT k.worker,k.executor,COALESCE(st.unavailable_until,''),COALESCE(st.reset_source,''),COALESCE(st.updated_at,''),
 COALESCE((SELECT w.last_seen_at FROM workers w WHERE w.name=k.worker ORDER BY julianday(w.last_seen_at) DESC LIMIT 1),'')
FROM (SELECT w.name AS worker,e.executor AS executor FROM worker_executors e JOIN workers w ON w.instance_id=e.worker_instance
      UNION SELECT worker,executor FROM executor_state) k
LEFT JOIN executor_state st ON st.worker=k.worker AND st.executor=k.executor
ORDER BY k.worker,k.executor`)
	if err != nil {
		return nil, fmt.Errorf("read executor states: %w", err)
	}
	defer rows.Close()
	now := s.now().UTC()
	statuses := []ExecutorStatus{}
	for rows.Next() {
		var status ExecutorStatus
		var until, source, updatedAt, lastSeen string
		if err := rows.Scan(&status.Worker, &status.Executor, &until, &source, &updatedAt, &lastSeen); err != nil {
			return nil, err
		}
		status.ID = status.Worker + "/" + status.Executor
		status.State, status.Reason = executorStateOK, executorReasonAvailable
		status.Message = status.Executor + " on " + status.Worker + " is available."
		if since := parseOptionalTime(updatedAt); since != nil {
			status.Since = *since
		} else if since := parseOptionalTime(lastSeen); since != nil {
			status.Since = *since
		}
		if limit := parseOptionalTime(until); limit != nil && limit.After(now) {
			limited := limit.UTC()
			status.State, status.Reason = executorStateRateLimited, executorReasonLimited
			status.RateLimitedUntil = &limited
			status.Message = status.Executor + " on " + status.Worker + " is rate limited until " + limited.Format(time.RFC3339) + "."
			if source != "" {
				status.ResetSource = &source
			}
		}
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}
