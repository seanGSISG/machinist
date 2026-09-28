package controlplane

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestUsageRollup(t *testing.T) {
	server, webServer := newTestHTTPServer(t)
	defer webServer.Close()

	created := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	fixtures := []struct {
		job, run, executor, model, command, ticket string
		input, output, cached, reasoning           int64
		created                                    time.Time
	}{
		{"job-1", "run-1", "codex", "gpt-5", "build", "FAC-06", 100, 40, 20, 10, created},
		{"job-2", "run-2", "codex", "gpt-5", "review", "FAC-07", 50, 20, 10, 5, created.Add(time.Hour)},
		{"job-3", "run-3", "claude", "sonnet", "build", "FAC-06", 75, 30, 15, 7, created.Add(2 * time.Hour)},
		{"job-4", "run-4", "claude", "sonnet", "review", "", 25, 10, 5, 2, created.Add(-24 * time.Hour)},
	}
	for _, fixture := range fixtures {
		if _, err := server.store.db.ExecContext(t.Context(), `INSERT INTO jobs(id,prompt,repository,command,state,created_at,updated_at) VALUES(?, '', 'machinist', ?, 'succeeded', ?, ?)`, fixture.job, fixture.command, fixture.created.Format(time.RFC3339Nano), fixture.created.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if _, err := server.store.db.ExecContext(t.Context(), `INSERT INTO runs(id,job_id,command,command_hash,executor,model,repository,rendered_prompt,timeout_ms,state) VALUES(?,?,?,'hash',?,?,'machinist','',1000,'succeeded')`, fixture.run, fixture.job, fixture.command, fixture.executor, fixture.model); err != nil {
			t.Fatal(err)
		}
		if _, err := server.store.db.ExecContext(t.Context(), `INSERT INTO run_usage(run_id,model,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,created_at) VALUES(?,?,?,?,?,?,?)`, fixture.run, fixture.model, fixture.input, fixture.output, fixture.cached, fixture.reasoning, fixture.created.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if fixture.ticket != "" {
			if _, err := server.store.db.ExecContext(t.Context(), `INSERT INTO job_labels(job_id,label_key,value) VALUES(?,'ticket',?)`, fixture.job, fixture.ticket); err != nil {
				t.Fatal(err)
			}
		}
	}

	tests := []struct {
		groupBy string
		want    []usageRollupRow
	}{
		{"executor", []usageRollupRow{{"codex", 2, 150, 60, 30, 15}, {"claude", 2, 100, 40, 20, 9}}},
		{"model", []usageRollupRow{{"gpt-5", 2, 150, 60, 30, 15}, {"sonnet", 2, 100, 40, 20, 9}}},
		{"command", []usageRollupRow{{"build", 2, 175, 70, 35, 17}, {"review", 2, 75, 30, 15, 7}}},
		{"ticket", []usageRollupRow{{"FAC-06", 2, 175, 70, 35, 17}, {"FAC-07", 1, 50, 20, 10, 5}, {"", 1, 25, 10, 5, 2}}},
	}
	for _, test := range tests {
		t.Run(test.groupBy, func(t *testing.T) {
			response, err := http.Get(webServer.URL + "/api/v1/usage?group_by=" + test.groupBy)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", response.StatusCode)
			}
			var body usageRollupResponse
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.GroupBy != test.groupBy || body.Since != "" || !usageRowsEqual(body.Rows, test.want) {
				t.Fatalf("body = %#v, want rows %#v", body, test.want)
			}
		})
	}

	t.Run("since", func(t *testing.T) {
		since := created.Add(30 * time.Minute).Format(time.RFC3339)
		response, err := http.Get(webServer.URL + "/api/v1/usage?group_by=executor&since=" + since)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var body usageRollupResponse
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		want := []usageRollupRow{{"claude", 1, 75, 30, 15, 7}, {"codex", 1, 50, 20, 10, 5}}
		if response.StatusCode != http.StatusOK || body.Since != since || !usageRowsEqual(body.Rows, want) {
			t.Fatalf("status = %d, body = %#v", response.StatusCode, body)
		}
	})

	for _, target := range []string{"/api/v1/usage?group_by=worker", "/api/v1/usage?group_by=model&since=yesterday"} {
		response, err := http.Get(webServer.URL + target)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400", target, response.StatusCode)
		}
	}
}

func usageRowsEqual(got, want []usageRollupRow) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
