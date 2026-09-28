package controlplane

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestMigration6FreshDB(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 7 {
		t.Fatalf("schema version = %d, want 7", version)
	}

	for table, columns := range map[string][]string{
		"job_labels":     {"job_id", "label_key", "value"},
		"jobs":           {"supersedes_job_id"},
		"runs":           {"failure_class", "reset_at", "reset_source", "incarnation"},
		"workers":        {"incarnation"},
		"executor_state": {"worker", "executor", "unavailable_until", "backoff_seconds", "reset_source", "updated_at"},
		"run_usage":      {"run_id", "model", "input_tokens", "output_tokens", "cached_input_tokens", "reasoning_tokens", "created_at"},
		"events":         {"id", "type", "subject_kind", "subject_id", "cause", "payload_json", "created_at"},
		"run_log_tail":   {"run_id", "next_offset", "data", "truncated", "updated_at"},
	} {
		for _, column := range columns {
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Errorf("%s.%s count = %d, want 1", table, column, count)
			}
		}
	}

	for _, index := range []string{"job_labels_key_value", "events_type_created_at"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("index %s count = %d, want 1", index, count)
		}
	}
}

func TestMigration6UpgradesV5(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machinist.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE jobs (
 id TEXT PRIMARY KEY, prompt TEXT NOT NULL, repository TEXT NOT NULL, command TEXT NOT NULL,
 trigger_identity TEXT NOT NULL DEFAULT '', trigger_config_signature TEXT NOT NULL DEFAULT '',
 trigger_generation_id TEXT NOT NULL DEFAULT '', occurrence_key TEXT NOT NULL DEFAULT '',
 trigger_subject TEXT NOT NULL DEFAULT '', github_issue_title TEXT NOT NULL DEFAULT '',
 fixed_trigger INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE runs (
 id TEXT PRIMARY KEY, job_id TEXT NOT NULL REFERENCES jobs(id), command TEXT NOT NULL, command_hash TEXT NOT NULL,
 executor TEXT NOT NULL, model TEXT NOT NULL DEFAULT '', repository TEXT NOT NULL, rendered_prompt TEXT NOT NULL,
 timeout_ms INTEGER NOT NULL, state TEXT NOT NULL, worker_instance TEXT, worker_name TEXT NOT NULL DEFAULT '',
 lease_token TEXT, lease_expires_at INTEGER, exit_code INTEGER, error TEXT, result TEXT, events TEXT,
 started_at TEXT, completed_at TEXT, duration_millis INTEGER, token_usage INTEGER);
CREATE TABLE workers (instance_id TEXT PRIMARY KEY, name TEXT NOT NULL, last_seen_at TEXT NOT NULL);
INSERT INTO jobs(id,prompt,repository,command,state,created_at,updated_at)
 VALUES('job_kept','prompt','repo','command','succeeded','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
INSERT INTO runs(id,job_id,command,command_hash,executor,repository,rendered_prompt,timeout_ms,state)
 VALUES('run_kept','job_kept','command','hash','codex','repo','rendered',1000,'succeeded');
PRAGMA user_version=5;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version, jobs, runs int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE id='job_kept' AND prompt='prompt'`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE id='run_kept' AND job_id='job_kept' AND rendered_prompt='rendered'`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if version != 7 || jobs != 1 || runs != 1 {
		t.Fatalf("upgraded version=%d jobs=%d runs=%d, want 7, 1, 1", version, jobs, runs)
	}
}

func TestMigration6RouteStubs(t *testing.T) {
	_, web := newTestHTTPServer(t)
	defer web.Close()

	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/usage"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			request, err := http.NewRequest(test.method, web.URL+test.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer secret")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusNotImplemented {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusNotImplemented)
			}
		})
	}
}

func TestAppendEvent(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	createdAt := time.Date(2026, time.September, 27, 12, 30, 0, 123, time.UTC)
	tx, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{
		Type: "usage", SubjectKind: "run", SubjectID: "run_1", Cause: "completion",
		Payload: map[string]any{"input_tokens": float64(42)}, CreatedAt: createdAt,
	}
	if err := store.AppendEvent(t.Context(), tx, event); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var eventType, subjectKind, subjectID, cause, payloadJSON, storedCreatedAt string
	if err := store.db.QueryRow(`SELECT type,subject_kind,subject_id,cause,payload_json,created_at FROM events`).Scan(
		&eventType, &subjectKind, &subjectID, &cause, &payloadJSON, &storedCreatedAt); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if eventType != event.Type || subjectKind != event.SubjectKind || subjectID != event.SubjectID || cause != event.Cause ||
		payload["input_tokens"] != float64(42) || storedCreatedAt != createdAt.Format(time.RFC3339Nano) {
		t.Fatalf("stored event = %q %q %q %q %s %q", eventType, subjectKind, subjectID, cause, payloadJSON, storedCreatedAt)
	}
}
