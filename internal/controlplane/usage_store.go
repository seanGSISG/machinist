package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

func (s *Store) writeRunUsage(ctx context.Context, tx *sql.Tx, runID string, usage *protocol.Usage, createdAt time.Time) error {
	if usage == nil {
		return nil
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CachedInputTokens < 0 || usage.ReasoningTokens < 0 {
		return errors.New("usage token counts must be non-negative")
	}
	created := createdAt.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_usage(run_id,model,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,created_at) VALUES(?,?,?,?,?,?,?)`,
		runID, usage.Model, usage.InputTokens, usage.OutputTokens, usage.CachedInputTokens, usage.ReasoningTokens, created); err != nil {
		return err
	}
	return s.AppendEvent(ctx, tx, Event{
		Type:        "usage",
		SubjectKind: "run",
		SubjectID:   runID,
		Cause:       "worker",
		Payload:     usage,
		CreatedAt:   createdAt,
	})
}
