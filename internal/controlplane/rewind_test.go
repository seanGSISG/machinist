package controlplane

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
	_ "modernc.org/sqlite"
)

func TestMigration7(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
		assertMigration7(t, store.db)
	})

	t.Run("v6 upgrade", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "machinist.db")
		store := openTestStore(t, path)
		if _, err := store.db.Exec(`
DROP TABLE workflow_loop_counters;
ALTER TABLE workflow_attempts DROP COLUMN attempt;
ALTER TABLE workflow_attempts DROP COLUMN previous_run_id;
ALTER TABLE workflow_attempts DROP COLUMN reason;
INSERT INTO jobs(id,prompt,repository,command,state,created_at,updated_at)
 VALUES('job_kept','prompt','repo','deliver','succeeded','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
INSERT INTO runs(id,job_id,command,command_hash,executor,repository,rendered_prompt,timeout_ms,state)
 VALUES('run_kept','job_kept','triage','hash','codex','repo','rendered',1000,'succeeded');
INSERT INTO workflow_attempts(run_id,step,outcome,summary) VALUES('run_kept',0,'complete','done');
PRAGMA user_version=6;`); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}

		upgraded := openTestStore(t, path)
		assertMigration7(t, upgraded.db)
		var outcome, summary string
		var attempt, previous, reason sql.NullString
		if err := upgraded.db.QueryRow(`SELECT outcome,summary,attempt,previous_run_id,reason FROM workflow_attempts WHERE run_id='run_kept'`).Scan(&outcome, &summary, &attempt, &previous, &reason); err != nil {
			t.Fatal(err)
		}
		if outcome != "complete" || summary != "done" || attempt.Valid || previous.Valid || reason.Valid {
			t.Fatalf("kept attempt = %q %q %v %v %v", outcome, summary, attempt, previous, reason)
		}
	})
}

func assertMigration7(t *testing.T, db *sql.DB) {
	t.Helper()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 7 {
		t.Fatalf("schema version = %d, want 7", version)
	}
	for table, columns := range map[string][]string{
		"workflow_attempts":      {"attempt", "previous_run_id", "reason"},
		"workflow_loop_counters": {"job_id", "edge", "count"},
	} {
		for _, column := range columns {
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Errorf("%s.%s count = %d, want 1", table, column, count)
			}
		}
	}
	var primaryKey int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('workflow_loop_counters') WHERE pk>0 AND name IN ('job_id','edge')`).Scan(&primaryKey); err != nil {
		t.Fatal(err)
	}
	if primaryKey != 2 {
		t.Errorf("workflow_loop_counters primary key columns = %d, want 2", primaryKey)
	}
}

func TestRewindAppends(t *testing.T) {
	server, web := newTestHTTPServer(t)
	defer web.Close()
	store := server.store
	id, err := store.createWorkflowJob(t.Context(), "issue URL", "machinist", "deliver",
		[]config.WorkflowStep{{Command: testAgent("triage", "issue URL")}, {Command: testAgent("build", "issue URL")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	triage := pollWorkflowStep(t, store)
	finishStep(t, store, triage, "complete")
	finishStep(t, store, pollWorkflowStep(t, store), "complete")
	assertJobState(t, store, id, "succeeded", 1)

	before := historyRows(t, store.db, id)
	status, body := postRewind(t, web.URL, id, map[string]any{"step": 0, "reason": "rewind"})
	if status != http.StatusAccepted || body != (rewindResponse{JobID: id, Step: 0, Attempt: 2}) {
		t.Fatalf("rewind = %d %#v", status, body)
	}
	after := historyRows(t, store.db, id)
	assertAppendedOnly(t, before, after, 1)
	assertLatestAttempt(t, store.db, id, 0, 2, triage.ID, "rewind")
	assertJobState(t, store, id, "queued", 0)

	for _, test := range []struct {
		name   string
		job    string
		body   map[string]any
		status int
	}{
		{"unknown job", "job_missing", map[string]any{"step": 0, "reason": "rewind"}, http.StatusNotFound},
		{"unknown step index", id, map[string]any{"step": 5, "reason": "rewind"}, http.StatusBadRequest},
		{"unknown step name", id, map[string]any{"step": "deploy", "reason": "rewind"}, http.StatusBadRequest},
		{"unknown reason", id, map[string]any{"step": 0, "reason": "undo"}, http.StatusBadRequest},
		{"active in step", id, map[string]any{"step": "triage", "reason": "retry"}, http.StatusConflict},
		{"step not reached", id, map[string]any{"step": "build", "reason": "retry"}, http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			if status, _ := postRewind(t, web.URL, test.job, test.body); status != test.status {
				t.Fatalf("status = %d, want %d", status, test.status)
			}
		})
	}
	assertAppendedOnly(t, after, historyRows(t, store.db, id), 0)

	direct, err := store.CreateJob(t.Context(), "direct", "elsewhere", "plan", testAgent("plan", "direct"))
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := postRewind(t, web.URL, direct, map[string]any{"step": 0, "reason": "rewind"}); status != http.StatusConflict {
		t.Fatalf("direct job status = %d, want %d", status, http.StatusConflict)
	}

	// Rewinding while a later step is in flight cancels that run first.
	second := pollWorkflowStep(t, store)
	finishStep(t, store, second, "complete")
	inFlight := pollWorkflowStep(t, store)
	before = historyRows(t, store.db, id)
	status, body = postRewind(t, web.URL, id, map[string]any{"step": "triage", "reason": "retry"})
	if status != http.StatusAccepted || body != (rewindResponse{JobID: id, Step: 0, Attempt: 3}) {
		t.Fatalf("rewind in flight = %d %#v", status, body)
	}
	after = historyRows(t, store.db, id)
	delete(before, "run:"+inFlight.ID)
	delete(after, "run:"+inFlight.ID)
	assertAppendedOnly(t, before, after, 1)
	assertLatestAttempt(t, store.db, id, 0, 3, second.ID, "retry")
	assertJobState(t, store, id, "queued", 0)
	var runState string
	if err := store.db.QueryRow(`SELECT state FROM runs WHERE id=?`, inFlight.ID).Scan(&runState); err != nil {
		t.Fatal(err)
	}
	if runState != "cancelled" {
		t.Fatalf("in-flight run state = %q, want cancelled", runState)
	}
	var events int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type='rewind' AND subject_id=?`, id).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("rewind events = %d, want 2", events)
	}
}

func pollWorkflowStep(t *testing.T, store *Store) *protocol.RunSpec {
	t.Helper()
	run, err := store.Poll(t.Context(), workflowWorker())
	if err != nil || run == nil {
		t.Fatalf("poll = %v %v", run, err)
	}
	return run
}

func postRewind(t *testing.T, endpoint, id string, body any) (int, rewindResponse) {
	t.Helper()
	response := postJSON(t, endpoint+"/api/v1/jobs/"+id+"/rewind", body, map[string]string{"Authorization": "Bearer secret"})
	defer response.Body.Close()
	var result rewindResponse
	if response.StatusCode == http.StatusAccepted {
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
	}
	return response.StatusCode, result
}

// historyRows renders every run and attempt column of a job so later rows can
// be compared byte for byte.
func historyRows(t *testing.T, db *sql.DB, job string) map[string]string {
	t.Helper()
	rows := map[string]string{}
	for prefix, query := range map[string]string{
		"run":     `SELECT * FROM runs WHERE job_id=?`,
		"attempt": `SELECT a.* FROM workflow_attempts a JOIN runs r ON r.id=a.run_id WHERE r.job_id=?`,
	} {
		result, err := db.Query(query, job)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := result.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for result.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := result.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			rendered := make([]string, len(values))
			for i, value := range values {
				rendered[i] = fmt.Sprintf("%s=%#v", columns[i], value)
			}
			rows[fmt.Sprintf("%s:%v", prefix, values[0])] = strings.Join(rendered, "|")
		}
		if err := result.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return rows
}

func assertAppendedOnly(t *testing.T, before, after map[string]string, appendedAttempts int) {
	t.Helper()
	if len(after) != len(before)+2*appendedAttempts {
		t.Fatalf("history rows = %d, want %d", len(after), len(before)+2*appendedAttempts)
	}
	for key, row := range before {
		if after[key] != row {
			t.Fatalf("history row %s changed:\n%s\n%s", key, row, after[key])
		}
	}
}

func assertLatestAttempt(t *testing.T, db *sql.DB, job string, step, attempt int, previous, reason string) {
	t.Helper()
	var gotStep, gotAttempt int
	var gotPrevious, gotReason, state string
	if err := db.QueryRow(`SELECT a.step,a.attempt,a.previous_run_id,a.reason,r.state FROM runs r JOIN workflow_attempts a ON a.run_id=r.id WHERE r.job_id=? ORDER BY r.rowid DESC LIMIT 1`, job).Scan(&gotStep, &gotAttempt, &gotPrevious, &gotReason, &state); err != nil {
		t.Fatal(err)
	}
	if gotStep != step || gotAttempt != attempt || gotPrevious != previous || gotReason != reason || state != "queued" {
		t.Fatalf("latest attempt = step %d attempt %d previous %q reason %q state %q", gotStep, gotAttempt, gotPrevious, gotReason, state)
	}
}

func assertJobState(t *testing.T, store *Store, job, state string, step int) {
	t.Helper()
	var gotState string
	var gotStep int
	if err := store.db.QueryRow(`SELECT j.state,w.current_step FROM jobs j JOIN workflow_jobs w ON w.job_id=j.id WHERE j.id=?`, job).Scan(&gotState, &gotStep); err != nil {
		t.Fatal(err)
	}
	if gotState != state || gotStep != step {
		t.Fatalf("job = %q step %d, want %q step %d", gotState, gotStep, state, step)
	}
}
