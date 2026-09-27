package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/owainlewis/machinist/internal/protocol"
)

// registerWorkerProcess records a poll and returns the incarnation that may
// lease work. Workers normally supply a per-process value. Zero is retained
// for older callers and is assigned the current control-plane value.
func registerWorkerProcess(ctx context.Context, tx *sql.Tx, request protocol.PollRequest, now string) (int64, bool, error) {
	var incarnation int64
	err := tx.QueryRowContext(ctx, `SELECT incarnation FROM workers WHERE instance_id=?`, request.InstanceID).Scan(&incarnation)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	if err == sql.ErrNoRows {
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

	if _, err := tx.ExecContext(ctx, `UPDATE runs SET state='interrupted',error='Worker process restarted',completed_at=? WHERE worker_instance=? AND state='running' AND COALESCE(incarnation,0)<?`, now, request.InstanceID, incarnation); err != nil {
		return 0, false, fmt.Errorf("interrupt runs from older worker incarnation: %w", err)
	}
	return incarnation, true, nil
}
