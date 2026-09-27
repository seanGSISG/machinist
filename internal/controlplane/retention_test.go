package controlplane

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestStatusIsReadOnly(t *testing.T) {
	server, web := newTestHTTPServer(t)
	defer web.Close()

	now := time.Now().UTC()
	expiresAt := now.Add(-time.Minute).UnixNano()
	createdAt := now.Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := server.store.db.ExecContext(t.Context(), `INSERT INTO jobs(id,prompt,repository,command,state,created_at,updated_at)
VALUES('job-expired','prompt','repository','plan','running',?,?)`, createdAt, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.ExecContext(t.Context(), `INSERT INTO runs(id,job_id,command,command_hash,executor,repository,rendered_prompt,timeout_ms,state,worker_instance,worker_name,lease_token,lease_expires_at,started_at)
VALUES('run-expired','job-expired','plan','hash','test','repository','prompt',60000,'running','worker-1','worker','lease-1',?,?)`, expiresAt, createdAt); err != nil {
		t.Fatal(err)
	}

	response, err := web.Client().Get(web.URL + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/status status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	var state, workerInstance, workerName, leaseToken, startedAt string
	var leaseExpiresAt int64
	if err := server.store.db.QueryRowContext(t.Context(), `SELECT state,worker_instance,worker_name,lease_token,lease_expires_at,started_at FROM runs WHERE id='run-expired'`).Scan(
		&state, &workerInstance, &workerName, &leaseToken, &leaseExpiresAt, &startedAt,
	); err != nil {
		t.Fatal(err)
	}
	if state != "running" || workerInstance != "worker-1" || workerName != "worker" || leaseToken != "lease-1" || leaseExpiresAt != expiresAt || startedAt != createdAt {
		t.Fatalf("expired lease changed after status GET: state=%q worker_instance=%q worker_name=%q lease_token=%q lease_expires_at=%d started_at=%q",
			state, workerInstance, workerName, leaseToken, leaseExpiresAt, startedAt)
	}
}

func TestRetentionPrune(t *testing.T) {
	for _, test := range []struct {
		name          string
		retentionDays *int
		oldDays       int
		newDays       int
	}{
		{name: "default", oldDays: 91, newDays: 89},
		{name: "custom", retentionDays: intPointer(30), oldDays: 31, newDays: 29},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			store := openTestStore(t, filepath.Join(directory, "machinist.db"))
			definitionPath := filepath.Join(directory, "config.toml")
			definition := ""
			if test.retentionDays != nil {
				definition = fmt.Sprintf("[server]\nretention_days = %d\n", *test.retentionDays)
			}
			if err := os.WriteFile(definitionPath, []byte(definition), 0o600); err != nil {
				t.Fatal(err)
			}
			server := &Server{store: store, definitionPath: definitionPath}

			synctest.Test(t, func(t *testing.T) {
				now := time.Now().UTC()
				insertRetentionRows(t, store.db, "old", now.AddDate(0, 0, -test.oldDays))
				insertRetentionRows(t, store.db, "new", now.AddDate(0, 0, -test.newDays))

				if err := server.pruneRetention(t.Context(), now); err != nil {
					t.Fatal(err)
				}
				for _, table := range []string{"events", "run_usage", "run_log_tail"} {
					if got := retainedRowCount(t, store.db, table, "old"); got != 0 {
						t.Errorf("%s old row count = %d, want 0", table, got)
					}
					if got := retainedRowCount(t, store.db, table, "new"); got != 1 {
						t.Errorf("%s new row count = %d, want 1", table, got)
					}
				}
			})
		})
	}
}

func insertRetentionRows(t *testing.T, db *sql.DB, id string, timestamp time.Time) {
	t.Helper()
	storedAt := timestamp.Format(time.RFC3339Nano)
	for _, insertion := range []string{
		`INSERT INTO events(type,subject_kind,subject_id,cause,payload_json,created_at) VALUES('test','run',?,'test','{}',?)`,
		`INSERT INTO run_usage(run_id,model,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,created_at) VALUES(?,'test',1,1,0,0,?)`,
		`INSERT INTO run_log_tail(run_id,next_offset,data,truncated,updated_at) VALUES(?,0,X'',0,?)`,
	} {
		if _, err := db.ExecContext(t.Context(), insertion, id, storedAt); err != nil {
			t.Fatal(err)
		}
	}
}

func retainedRowCount(t *testing.T, db *sql.DB, table, id string) int {
	t.Helper()
	column := "run_id"
	if table == "events" {
		column = "subject_id"
	}
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table+" WHERE "+column+"=?", id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func intPointer(value int) *int { return &value }
