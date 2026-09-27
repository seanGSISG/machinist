package controlplane

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

func TestCancelDirectJob(t *testing.T) {
	server, web := newTestHTTPServer(t)
	defer web.Close()

	id, err := server.store.CreateJob(t.Context(), "cancel me", "machinist", "plan", testAgent("plan", "cancel me"))
	if err != nil {
		t.Fatal(err)
	}
	assertCancelResponse(t, web.URL, id, http.StatusAccepted, "cancelled")
	assertCancelledJobAndEvent(t, server.store, id)
	assertCancelResponse(t, web.URL, id, http.StatusOK, "cancelled")
	assertEventCount(t, server.store, id, 1)

	finished, err := server.store.CreateJob(t.Context(), "done", "machinist", "plan", testAgent("plan", "done"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.Exec(`UPDATE jobs SET state='succeeded' WHERE id=?`, finished); err != nil {
		t.Fatal(err)
	}
	assertCancelResponse(t, web.URL, finished, http.StatusConflict, "succeeded")
	assertEventCount(t, server.store, finished, 0)
}

func TestCancelWorkflowJob(t *testing.T) {
	server, web := newTestHTTPServer(t)
	defer web.Close()
	step := config.WorkflowStep{Command: testAgent("plan", "workflow")}

	id, err := server.store.createWorkflowJob(t.Context(), "cancel me", "machinist", "workflow", []config.WorkflowStep{step}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertCancelResponse(t, web.URL, id, http.StatusAccepted, "cancelled")
	assertCancelledJobAndEvent(t, server.store, id)
	assertCancelResponse(t, web.URL, id, http.StatusOK, "cancelled")
	assertEventCount(t, server.store, id, 1)

	finished, err := server.store.createWorkflowJob(t.Context(), "done", "machinist", "workflow", []config.WorkflowStep{step}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.Exec(`UPDATE jobs SET state='succeeded' WHERE id=?`, finished); err != nil {
		t.Fatal(err)
	}
	assertCancelResponse(t, web.URL, finished, http.StatusConflict, "succeeded")
	assertEventCount(t, server.store, finished, 0)

	for _, state := range []string{"interrupted", "blocked", "failed"} {
		t.Run(state, func(t *testing.T) {
			id, err := server.store.createWorkflowJob(t.Context(), state, "machinist", "workflow", []config.WorkflowStep{step}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := server.store.db.Exec(`UPDATE jobs SET state=? WHERE id=?`, state, id); err != nil {
				t.Fatal(err)
			}
			assertCancelResponse(t, web.URL, id, http.StatusAccepted, "cancelled")
			assertEventCount(t, server.store, id, 1)
		})
	}
}

func TestCancelUnknownJob(t *testing.T) {
	_, web := newTestHTTPServer(t)
	defer web.Close()
	assertCancelResponse(t, web.URL, "job_missing", http.StatusNotFound, "")
}

func TestCancelJobTxRollsBackEventWithState(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	id, err := store.CreateJob(t.Context(), "cancel me", "machinist", "plan", testAgent("plan", "cancel me"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.cancelJobTx(t.Context(), tx, id)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("cancelJobTx status = %d, error = %v", status, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := store.db.QueryRow(`SELECT state FROM jobs WHERE id=?`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		t.Fatalf("state after rollback = %q", state)
	}
	assertEventCount(t, store, id, 0)
}

func assertCancelResponse(t *testing.T, endpoint, id string, wantStatus int, wantState string) {
	t.Helper()
	response := postJSON(t, endpoint+"/api/v1/jobs/"+id+"/cancel", nil, map[string]string{"Authorization": "Bearer secret"})
	defer response.Body.Close()
	var body cancelResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus || body.JobID != id || body.State != wantState {
		t.Fatalf("cancel %s = status %d body %#v, want %d state %q", id, response.StatusCode, body, wantStatus, wantState)
	}
}

func assertCancelledJobAndEvent(t *testing.T, store *Store, id string) {
	t.Helper()
	var jobState, runState string
	var exitCode int
	var rawCompletedAt string
	if err := store.db.QueryRow(`SELECT j.state,r.state,r.exit_code,r.completed_at FROM jobs j JOIN runs r ON r.job_id=j.id WHERE j.id=?`, id).Scan(&jobState, &runState, &exitCode, &rawCompletedAt); err != nil {
		t.Fatal(err)
	}
	completedAt, err := time.Parse(time.RFC3339Nano, rawCompletedAt)
	if err != nil {
		t.Fatal(err)
	}
	if jobState != "cancelled" || runState != "cancelled" || exitCode != 130 || completedAt.IsZero() {
		t.Fatalf("cancelled state = job %q run %q exit %d completed %v", jobState, runState, exitCode, completedAt)
	}
	assertEventCount(t, store, id, 1)
}

func assertEventCount(t *testing.T, store *Store, id string, want int) {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type='cancel' AND subject_kind='job' AND subject_id=?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("cancel event count for %s = %d, want %d", id, count, want)
	}
}
