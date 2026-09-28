package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/owainlewis/machinist/internal/config"
)

// loopBlockedWorkflow appends the configured earlier step while holding the
// completion transaction. The counter records loops taken, not blocks seen.
func loopBlockedWorkflow(ctx context.Context, tx *sql.Tx, job, repository, plan string, index int, now string) (bool, error) {
	var steps []config.WorkflowStep
	if err := json.Unmarshal([]byte(plan), &steps); err != nil {
		return false, err
	}
	if index < 0 || index >= len(steps) {
		return false, errors.New("invalid workflow step")
	}
	onBlocked := steps[index].OnBlocked
	if onBlocked == nil {
		return false, nil
	}
	target := -1
	for i := 0; i < index; i++ {
		if steps[i].ID == onBlocked.Goto {
			target = i
			break
		}
	}
	if target < 0 || onBlocked.Max < 1 {
		return false, errors.New("invalid on_blocked workflow edge")
	}

	edge := steps[index].ID + "->" + onBlocked.Goto
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count FROM workflow_loop_counters WHERE job_id=? AND edge=?`, job, edge).Scan(&count)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if count >= onBlocked.Max {
		return false, nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workflow_loop_counters(job_id,edge,count) VALUES(?,?,1)
		ON CONFLICT(job_id,edge) DO UPDATE SET count=count+1`, job, edge); err != nil {
		return false, err
	}
	if err = appendLoopAttempt(ctx, tx, job, repository, target, steps[target], now); err != nil {
		return false, err
	}
	return true, nil
}

// appendLoopAttempt applies the same append-only lineage used by explicit
// rewinds, while remaining inside the transaction that completed the blocked
// attempt.
func appendLoopAttempt(ctx context.Context, tx *sql.Tx, job, repository string, index int, step config.WorkflowStep, now string) error {
	var previousRun string
	var attempts, highest int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(a.attempt),0),COALESCE((SELECT r2.id FROM runs r2 JOIN workflow_attempts a2 ON a2.run_id=r2.id WHERE r2.job_id=? AND a2.step=? ORDER BY r2.rowid DESC LIMIT 1),'')
		FROM runs r JOIN workflow_attempts a ON a.run_id=r.id WHERE r.job_id=? AND a.step=?`, job, index, job, index).Scan(&attempts, &highest, &previousRun); err != nil {
		return err
	}
	attempt := max(attempts, highest) + 1
	if err := enqueueStep(ctx, tx, job, repository, index, step, false, now); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workflow_attempts SET attempt=?,previous_run_id=NULLIF(?,''),reason='loop' WHERE run_id=(SELECT id FROM runs WHERE job_id=? ORDER BY rowid DESC LIMIT 1)`, attempt, previousRun, job)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update loop attempt lineage: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("update loop attempt lineage: changed %d rows", changed)
	}
	return nil
}
