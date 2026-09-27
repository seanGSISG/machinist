package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/owainlewis/machinist/internal/protocol"
)

// registerWorkerProcess records a poll and returns the incarnation that may
// lease work. Workers normally supply a per-process value. For compatibility,
// zero-valued legacy callers reuse the current control-plane value; because
// they provide no process identity, their restarts cannot be fenced here.
func registerWorkerProcess(ctx context.Context, tx *sql.Tx, request protocol.PollRequest, now string) (int64, bool, error) {
	var incarnation int64
	err := tx.QueryRowContext(ctx, `SELECT incarnation FROM workers WHERE instance_id=?`, request.InstanceID).Scan(&incarnation)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		incarnation = request.Incarnation
		if incarnation == 0 {
			incarnation = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workers(instance_id,name,last_seen_at,incarnation) VALUES(?,?,?,?)`, request.InstanceID, request.Name, now, incarnation); err != nil {
			return 0, false, err
		}
	} else if request.Incarnation == 0 || request.Incarnation == incarnation {
		if _, err := tx.ExecContext(ctx, `UPDATE workers SET name=?,last_seen_at=? WHERE instance_id=?`, request.Name, now, request.InstanceID); err != nil {
			return 0, false, err
		}
	} else if request.Incarnation > incarnation {
		incarnation = request.Incarnation
		if _, err := tx.ExecContext(ctx, `UPDATE workers SET name=?,last_seen_at=?,incarnation=? WHERE instance_id=?`, request.Name, now, incarnation, request.InstanceID); err != nil {
			return 0, false, err
		}
	} else {
		return incarnation, false, nil
	}

	const reason = "Worker process restarted"
	olderRun := `r.worker_instance=? AND r.state='running' AND COALESCE(r.incarnation,0)<?`
	if _, err := tx.ExecContext(ctx, `UPDATE trigger_state SET last_job_state='failed',last_job_error=?,health='failed',latest_error=?,updated_at=? WHERE EXISTS (
		SELECT 1 FROM jobs j JOIN runs r ON r.job_id=j.id
		WHERE j.trigger_identity=trigger_state.identity AND j.trigger_generation_id=trigger_state.generation_id
		AND NOT EXISTS(SELECT 1 FROM workflow_jobs w WHERE w.job_id=j.id) AND `+olderRun+`)`, reason, reason, now, request.InstanceID, incarnation); err != nil {
		return 0, false, fmt.Errorf("record trigger failure after worker restart: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='failed',updated_at=? WHERE id IN (
		SELECT r.job_id FROM runs r WHERE `+olderRun+` AND NOT EXISTS(SELECT 1 FROM workflow_jobs w WHERE w.job_id=r.job_id))`, now, request.InstanceID, incarnation); err != nil {
		return 0, false, fmt.Errorf("fail jobs from older worker incarnation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='interrupted',updated_at=? WHERE id IN (
		SELECT r.job_id FROM runs r WHERE `+olderRun+` AND EXISTS(SELECT 1 FROM workflow_jobs w WHERE w.job_id=r.job_id))`, now, request.InstanceID, incarnation); err != nil {
		return 0, false, fmt.Errorf("interrupt workflow jobs from older worker incarnation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs AS r SET state='interrupted',error=?,lease_expires_at=NULL,completed_at=? WHERE `+olderRun, reason, now, request.InstanceID, incarnation); err != nil {
		return 0, false, fmt.Errorf("interrupt runs from older worker incarnation: %w", err)
	}
	return incarnation, true, nil
}
