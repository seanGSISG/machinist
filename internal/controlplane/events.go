package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

type Event struct {
	Type        string
	SubjectKind string
	SubjectID   string
	Cause       string
	Payload     any
	CreatedAt   time.Time
}

func (s *Store) AppendEvent(ctx context.Context, tx *sql.Tx, event Event) error {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO events(type,subject_kind,subject_id,cause,payload_json,created_at) VALUES(?,?,?,?,?,?)`,
		event.Type, event.SubjectKind, event.SubjectID, event.Cause, string(payload), event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	slog.InfoContext(ctx, "control plane event",
		"type", event.Type,
		"subject_kind", event.SubjectKind,
		"subject_id", event.SubjectID,
		"cause", event.Cause,
		"payload", string(payload),
		"created_at", event.CreatedAt.UTC(),
	)
	return nil
}
