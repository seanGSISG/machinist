package controlplane

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

func TestRateLimitRequeue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
		clock := newTestClock(time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC))
		store.now = clock.Now
		_, err := store.createLegacyWorkflowJob(t.Context(), "Fix it", "machinist", "fix", []config.WorkflowStep{{Command: testAgent("fix", "Fix it")}})
		if err != nil {
			t.Fatal(err)
		}
		request := workflowWorker()
		run, err := store.Poll(t.Context(), request)
		if err != nil || run == nil {
			t.Fatalf("lease run: %+v, %v", run, err)
		}
		resetAt := clock.Now().Add(5 * time.Minute)
		completion := protocol.Completion{
			InstanceID: request.InstanceID, LeaseToken: run.LeaseToken, State: "failed", ExitCode: 1,
			Error: "usage limit", Result: json.RawMessage(`{"error":"limited"}`), Events: "limited",
			FailureClass: "rate_limited", ResetAt: &resetAt, ResetSource: "structured",
			Usage: &protocol.Usage{Model: "gpt-5", InputTokens: 100, OutputTokens: 10},
		}
		if err := store.Complete(t.Context(), run.ID, completion); err != nil {
			t.Fatal(err)
		}

		var runState, jobState string
		var attempts int
		var failureClass, storedResetAt, resetSource sql.NullString
		if err := store.db.QueryRowContext(t.Context(), `SELECT r.state,j.state,r.failure_class,r.reset_at,r.reset_source,(SELECT COUNT(*) FROM workflow_attempts WHERE run_id=r.id) FROM runs r JOIN jobs j ON j.id=r.job_id WHERE r.id=?`, run.ID).Scan(
			&runState, &jobState, &failureClass, &storedResetAt, &resetSource, &attempts); err != nil {
			t.Fatal(err)
		}
		if runState != "queued" || jobState != "queued" || attempts != 1 {
			t.Fatalf("requeued state = run %q job %q attempts %d", runState, jobState, attempts)
		}
		if failureClass.String != completion.FailureClass || storedResetAt.String != resetAt.Format(time.RFC3339Nano) || resetSource.String != completion.ResetSource {
			t.Fatalf("run rate-limit fields = %q %q %q", failureClass.String, storedResetAt.String, resetSource.String)
		}
		var staleFields int
		if err := store.db.QueryRowContext(t.Context(), `SELECT (exit_code IS NOT NULL)+(error IS NOT NULL)+(result IS NOT NULL)+(events IS NOT NULL)+(completed_at IS NOT NULL)+(duration_millis IS NOT NULL)+(token_usage IS NOT NULL) FROM runs WHERE id=?`, run.ID).Scan(&staleFields); err != nil {
			t.Fatal(err)
		}
		if staleFields != 0 {
			t.Fatalf("requeued run retained %d terminal fields", staleFields)
		}
		var usageRows int
		if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM run_usage WHERE run_id=?`, run.ID).Scan(&usageRows); err != nil {
			t.Fatal(err)
		}
		if usageRows != 0 {
			t.Fatalf("rate-limited completion wrote %d usage rows", usageRows)
		}

		var worker, executor, until, source string
		var backoff int64
		if err := store.db.QueryRowContext(t.Context(), `SELECT worker,executor,unavailable_until,backoff_seconds,reset_source FROM executor_state`).Scan(&worker, &executor, &until, &backoff, &source); err != nil {
			t.Fatal(err)
		}
		if worker != request.Name || executor != run.Executor || until != resetAt.Format(time.RFC3339Nano) || backoff != 300 || source != completion.ResetSource {
			t.Fatalf("executor state = %q %q %q %d %q", worker, executor, until, backoff, source)
		}
		var eventType, subjectKind, subjectID, cause, payloadJSON string
		if err := store.db.QueryRowContext(t.Context(), `SELECT type,subject_kind,subject_id,cause,payload_json FROM events WHERE type='rate_limit_stall_start'`).Scan(&eventType, &subjectKind, &subjectID, &cause, &payloadJSON); err != nil {
			t.Fatal(err)
		}
		var payload map[string]string
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			t.Fatal(err)
		}
		if eventType != "rate_limit_stall_start" || subjectKind != "executor" || subjectID != request.Name+"/"+run.Executor || cause != "completion" ||
			payload["worker"] != request.Name || payload["executor"] != run.Executor || payload["until"] != until || payload["source"] != source {
			t.Fatalf("stall start event = %q %q %q %q %s", eventType, subjectKind, subjectID, cause, payloadJSON)
		}

		clock.Advance(5 * time.Minute)
		retried, err := store.Poll(t.Context(), request)
		if err != nil || retried == nil || retried.ID != run.ID {
			t.Fatalf("lease requeued run: %+v, %v", retried, err)
		}
		finalUsage := &protocol.Usage{Model: "gpt-5", InputTokens: 40, OutputTokens: 5}
		if err := store.Complete(t.Context(), retried.ID, protocol.Completion{
			InstanceID: request.InstanceID, LeaseToken: retried.LeaseToken, State: "succeeded",
			Result: json.RawMessage(`{"step_result":{"outcome":"complete","summary":"done"}}`), Usage: finalUsage,
		}); err != nil {
			t.Fatalf("complete re-leased run with usage: %v", err)
		}
		var model string
		var input, output int64
		if err := store.db.QueryRowContext(t.Context(), `SELECT model,input_tokens,output_tokens FROM run_usage WHERE run_id=?`, run.ID).Scan(&model, &input, &output); err != nil {
			t.Fatal(err)
		}
		if model != finalUsage.Model || input != finalUsage.InputTokens || output != finalUsage.OutputTokens {
			t.Fatalf("final usage = %q %d %d", model, input, output)
		}
	})
}

func TestLeaseSkipsLimitedExecutor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
		clock := newTestClock(time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC))
		store.now = clock.Now
		agent := testAgent("fix", "Fix it")
		if _, err := store.CreateJob(t.Context(), "Fix it", "machinist", "fix", agent); err != nil {
			t.Fatal(err)
		}
		request := pollRequest("worker-a", []string{agent.Executor}, []string{"machinist"})
		run, err := store.Poll(t.Context(), request)
		if err != nil || run == nil {
			t.Fatalf("initial lease: %+v, %v", run, err)
		}
		resetAt := clock.Now().Add(time.Minute)
		if err := store.Complete(t.Context(), run.ID, protocol.Completion{
			InstanceID: request.InstanceID, LeaseToken: run.LeaseToken, State: "failed", ExitCode: 1,
			FailureClass: "rate_limited", ResetAt: &resetAt, ResetSource: "regex",
		}); err != nil {
			t.Fatal(err)
		}
		if leased, err := store.Poll(t.Context(), request); err != nil || leased != nil {
			t.Fatalf("lease before reset = %+v, %v", leased, err)
		}
		otherExecutor := testAgent("other", "Other work")
		otherExecutor.Executor = "claude"
		if _, err := store.CreateJob(t.Context(), "Other work", "machinist", "other", otherExecutor); err != nil {
			t.Fatal(err)
		}
		sameWorker := pollRequest("worker-a-other", []string{otherExecutor.Executor}, []string{"machinist"})
		if other, err := store.Poll(t.Context(), sameWorker); err != nil || other == nil || other.Executor != otherExecutor.Executor {
			t.Fatalf("other executor on same worker = %+v, %v", other, err)
		}
		if _, err := store.CreateJob(t.Context(), "Same executor", "machinist", "same", agent); err != nil {
			t.Fatal(err)
		}
		otherWorker := pollRequest("worker-b", []string{agent.Executor}, []string{"machinist"})
		otherWorker.Name = "other-worker"
		other, err := store.Poll(t.Context(), otherWorker)
		if err != nil || other == nil || other.ID != run.ID {
			t.Fatalf("same executor on other worker = %+v, %v", other, err)
		}
		if err := store.Complete(t.Context(), other.ID, protocol.Completion{
			InstanceID: otherWorker.InstanceID, LeaseToken: other.LeaseToken, State: "succeeded",
		}); err != nil {
			t.Fatalf("complete on other worker: %v", err)
		}

		// A fractional current time also guards against comparing trimmed
		// RFC3339Nano timestamps lexicographically.
		clock.Advance(time.Minute + 500*time.Millisecond)
		leased, err := store.Poll(t.Context(), request)
		if err != nil || leased == nil || leased.ID == run.ID || leased.Executor != agent.Executor {
			t.Fatalf("lease after reset = %+v, %v", leased, err)
		}
		var unavailable sql.NullString
		var backoff int64
		if err := store.db.QueryRowContext(t.Context(), `SELECT unavailable_until,backoff_seconds FROM executor_state WHERE worker=? AND executor=?`, request.Name, agent.Executor).Scan(&unavailable, &backoff); err != nil {
			t.Fatal(err)
		}
		if unavailable.Valid || backoff != 0 {
			t.Fatalf("expired stall state = unavailable %q backoff %d", unavailable.String, backoff)
		}
		if again, err := store.Poll(t.Context(), request); err != nil || again == nil || again.ID != leased.ID {
			t.Fatalf("repeat active lease = %+v, %v", again, err)
		}
		var endEvents int
		var cause string
		if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*),COALESCE(MAX(cause),'') FROM events WHERE type='rate_limit_stall_end'`).Scan(&endEvents, &cause); err != nil {
			t.Fatal(err)
		}
		if endEvents != 1 || cause != "expired" {
			t.Fatalf("stall end events = %d cause %q", endEvents, cause)
		}
	})
}
