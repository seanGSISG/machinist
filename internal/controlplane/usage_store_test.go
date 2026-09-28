package controlplane

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
)

func TestRunUsageWritten(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	jobID, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan request"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil {
		t.Fatal(err)
	}
	usage := &protocol.Usage{Model: "gpt-5", InputTokens: 100, OutputTokens: 25, CachedInputTokens: 80, ReasoningTokens: 7}
	completion := protocol.Completion{
		InstanceID: "worker-a",
		LeaseToken: run.LeaseToken,
		State:      "succeeded",
		Usage:      usage,
	}
	if err := store.Complete(t.Context(), run.ID, completion); err != nil {
		t.Fatal(err)
	}
	// A retried delivery is idempotent and must not append a second usage event.
	if err := store.Complete(t.Context(), run.ID, completion); err != nil {
		t.Fatal(err)
	}

	var model, created string
	var input, output, cached, reasoning int64
	if err := store.db.QueryRowContext(t.Context(), `SELECT model,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,created_at FROM run_usage WHERE run_id=?`, run.ID).
		Scan(&model, &input, &output, &cached, &reasoning, &created); err != nil {
		t.Fatal(err)
	}
	if model != usage.Model || input != usage.InputTokens || output != usage.OutputTokens || cached != usage.CachedInputTokens || reasoning != usage.ReasoningTokens || created == "" {
		t.Fatalf("stored usage = %q %d %d %d %d %q", model, input, output, cached, reasoning, created)
	}

	var eventCount int
	var subjectKind, subjectID, cause, payload string
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*),subject_kind,subject_id,cause,payload_json FROM events WHERE type='usage' AND subject_id=?`, run.ID).
		Scan(&eventCount, &subjectKind, &subjectID, &cause, &payload); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 || subjectKind != "run" || subjectID != run.ID || cause != "worker" {
		t.Fatalf("usage event = count %d, kind %q, id %q, cause %q", eventCount, subjectKind, subjectID, cause)
	}
	var eventUsage protocol.Usage
	if err := json.Unmarshal([]byte(payload), &eventUsage); err != nil || eventUsage != *usage {
		t.Fatalf("usage payload = %#v, %v", eventUsage, err)
	}

	var storedJob string
	if err := store.db.QueryRowContext(t.Context(), `SELECT job_id FROM runs WHERE id=?`, run.ID).Scan(&storedJob); err != nil || storedJob != jobID {
		t.Fatalf("completed run job = %q, %v", storedJob, err)
	}
}
