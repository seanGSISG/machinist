package controlplane

import (
	"context"
	"fmt"
	"time"
)

func (s *Server) pruneRetention(ctx context.Context, now time.Time) error {
	definition, _, err := s.loadDefinitionFile()
	if err != nil {
		return fmt.Errorf("load retention configuration: %w", err)
	}
	cutoff := now.UTC().AddDate(0, 0, -definition.Server.RetentionDayLimit()).Format(time.RFC3339Nano)

	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("prune retained records: %w", err)
	}
	defer tx.Rollback()
	for _, deletion := range []struct {
		table  string
		column string
	}{
		{table: "events", column: "created_at"},
		{table: "run_usage", column: "created_at"},
		{table: "run_log_tail", column: "updated_at"},
	} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+deletion.table+" WHERE "+deletion.column+" < ?", cutoff); err != nil {
			return fmt.Errorf("prune %s: %w", deletion.table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("prune retained records: %w", err)
	}
	return nil
}
