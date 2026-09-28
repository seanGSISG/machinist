package controlplane

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestExecutorClear(t *testing.T) {
	server, web := newTestHTTPServer(t)
	defer web.Close()
	if _, err := server.store.Poll(t.Context(), pollRequest("worker-a", []string{"claude", "codex"}, []string{"machinist"})); err != nil {
		t.Fatal(err)
	}
	until := server.store.now().UTC().Add(30 * time.Minute).Truncate(time.Second)
	if _, err := server.store.db.Exec(`INSERT INTO executor_state(worker,executor,unavailable_until,backoff_seconds,reset_source,updated_at) VALUES('test','codex',?,60,'estimate',?)`,
		until.Format(time.RFC3339Nano), server.store.now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	executors := getStatus(t, web.URL).Executors
	if len(executors) != 2 || executors[0].ID != "test/claude" || executors[0].State != "ok" || executors[0].RateLimitedUntil != nil || executors[0].ResetSource != nil {
		t.Fatalf("executors = %+v", executors)
	}
	limited := executors[1]
	if limited.ID != "test/codex" || limited.Worker != "test" || limited.Executor != "codex" || limited.State != "rate_limited" || limited.Reason != "RateLimited" ||
		limited.RateLimitedUntil == nil || !limited.RateLimitedUntil.Equal(until) || limited.ResetSource == nil || *limited.ResetSource != "estimate" || limited.Message == "" || limited.Since.IsZero() {
		t.Fatalf("limited executor = %+v", limited)
	}

	endpoint := web.URL + "/api/v1/executors/test/codex/clear-rate-limit"
	for name, headers := range map[string]map[string]string{
		"no credentials": nil,
		"wrong token":    {"Authorization": "Bearer wrong"},
		"cross origin":   {"Origin": "https://evil.example", "X-Machinist-CSRF": server.csrfToken},
	} {
		response := postJSON(t, endpoint, nil, headers)
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden {
			t.Fatalf("%s clear status = %d, want 401 or 403", name, response.StatusCode)
		}
	}
	if status := getStatus(t, web.URL).Executors[1].State; status != "rate_limited" {
		t.Fatalf("unauthorized clear changed state to %q", status)
	}

	assertClearResponse(t, endpoint, http.StatusOK)
	executors = getStatus(t, web.URL).Executors
	if cleared := executors[1]; cleared.State != "ok" || cleared.Reason != "Available" || cleared.RateLimitedUntil != nil || cleared.ResetSource != nil {
		t.Fatalf("cleared executor = %+v", cleared)
	}
	var stored *string
	if err := server.store.db.QueryRow(`SELECT unavailable_until FROM executor_state WHERE worker='test' AND executor='codex'`).Scan(&stored); err != nil || stored != nil {
		t.Fatalf("unavailable_until = %v, %v", stored, err)
	}
	assertStallEndEvents(t, server.store, 1)

	assertClearResponse(t, endpoint, http.StatusOK)
	assertClearResponse(t, web.URL+"/api/v1/executors/test/claude/clear-rate-limit", http.StatusOK)
	assertStallEndEvents(t, server.store, 1)

	for _, path := range []string{"/api/v1/executors/test/gemini/clear-rate-limit", "/api/v1/executors/other/codex/clear-rate-limit"} {
		response := postJSON(t, web.URL+path, nil, map[string]string{"Authorization": "Bearer secret"})
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("clear %s status = %d, want 404", path, response.StatusCode)
		}
	}
}

func assertClearResponse(t *testing.T, endpoint string, want int) {
	t.Helper()
	response := postJSON(t, endpoint, nil, map[string]string{"Authorization": "Bearer secret"})
	defer response.Body.Close()
	var body clearRateLimitResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want || body.Worker != "test" || body.State != "ok" || body.Executor == "" {
		t.Fatalf("clear = status %d body %#v", response.StatusCode, body)
	}
}

func assertStallEndEvents(t *testing.T, store *Store, want int) {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type='rate_limit_stall_end' AND subject_kind='executor' AND subject_id='test/codex' AND cause='operator'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("operator stall_end events = %d, want %d", count, want)
	}
}
