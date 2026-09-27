package controlplane

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var updateReadAPIGolden = flag.Bool("update", false, "update read API golden files")

const readAPITime = "2026-09-27T12:00:00Z"

func TestStatusSlim(t *testing.T) {
	server, web := newTestHTTPServer(t)
	defer web.Close()
	definition, err := os.OpenFile(server.definitionPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := definition.WriteString("\n[server]\nfinished_limit = 2\n"); err != nil {
		t.Fatal(err)
	}
	if err := definition.Close(); err != nil {
		t.Fatal(err)
	}

	for index, state := range []string{"succeeded", "failed", "cancelled", "running", "queued"} {
		insertReadAPIJob(t, server, fmt.Sprintf("job_%d", index), state, "TOP SECRET PROMPT")
	}
	if _, err := server.store.db.Exec(`INSERT INTO job_labels(job_id,label_key,value) VALUES('job_4','ticket','FAC-06'); INSERT INTO task_inputs(job_id,task) VALUES('job_4','{"title":"Safe title","source_url":"https://example.test/ticket","spec":"TOP SECRET SPEC"}')`); err != nil {
		t.Fatal(err)
	}

	statusBody := getReadAPIBody(t, web.URL+"/api/v1/status", nil, http.StatusOK)
	if bytes.Contains(statusBody, []byte("TOP SECRET PROMPT")) || bytes.Contains(statusBody, []byte("TOP SECRET SPEC")) || bytes.Contains(statusBody, []byte("rendered secret")) {
		t.Fatalf("status leaked detail text: %s", statusBody)
	}
	var status StatusResponse
	if err := json.Unmarshal(statusBody, &status); err != nil {
		t.Fatal(err)
	}
	if status.SchemaVersion != 1 || status.GeneratedAt.IsZero() || len(status.Jobs) != 4 {
		t.Fatalf("status schema=%d generated=%v jobs=%d, want 1, set, 4", status.SchemaVersion, status.GeneratedAt, len(status.Jobs))
	}
	if status.Executors == nil || status.GatesAwaitingApproval == nil || status.BlockedJobs == nil || status.Logins == nil {
		t.Fatal("status v-schema arrays must never be null")
	}

	jobsBody := getReadAPIBody(t, web.URL+"/api/v1/jobs?state=queued&label=ticket:FAC-06&limit=1", nil, http.StatusOK)
	if bytes.Contains(jobsBody, []byte("TOP SECRET PROMPT")) || bytes.Contains(jobsBody, []byte("TOP SECRET SPEC")) || bytes.Contains(jobsBody, []byte("rendered secret")) {
		t.Fatalf("jobs list leaked detail text: %s", jobsBody)
	}
	var jobs jobsResponse
	if err := json.Unmarshal(jobsBody, &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs.Jobs) != 1 || jobs.Jobs[0].ID != "job_4" {
		t.Fatalf("filtered jobs = %#v", jobs.Jobs)
	}
}

func TestStatusSchemaGolden(t *testing.T) {
	server, web := newTestHTTPServer(t)
	defer web.Close()
	server.store.now = func() time.Time { return time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC) }
	insertReadAPIJob(t, server, "job_golden", "running", "golden prompt")
	insertReadAPIRun(t, server, "run_golden", "job_golden")

	status := decodeObject(t, getReadAPIBody(t, web.URL+"/api/v1/status", nil, http.StatusOK))
	status["csrf_token"] = "<csrf>"
	job := decodeObject(t, getReadAPIBody(t, web.URL+"/api/v1/jobs/job_golden", workerAuthorization(), http.StatusOK))
	run := decodeObject(t, getReadAPIBody(t, web.URL+"/api/v1/runs/run_golden", workerAuthorization(), http.StatusOK))
	actual, err := json.MarshalIndent(map[string]any{"status": status, "job_detail": job, "run_detail": run}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	actual = append(actual, '\n')
	golden := filepath.Join("testdata", "status_schema.golden.json")
	if *updateReadAPIGolden {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, actual, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, want) {
		t.Fatalf("schema golden mismatch (-update to refresh)\nactual:\n%s\nwant:\n%s", actual, want)
	}
}

func TestJobDetail(t *testing.T) {
	server, web := newTestHTTPServer(t)
	defer web.Close()
	insertReadAPIJob(t, server, "job_detail", "running", "detail prompt")
	for index := range 25 {
		insertReadAPIRun(t, server, fmt.Sprintf("run_%02d", index), "job_detail")
	}
	body := getReadAPIBody(t, web.URL+"/api/v1/jobs/job_detail", workerAuthorization(), http.StatusOK)
	var detail jobDetailResponse
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Prompt != "detail prompt" || len(detail.Runs) != 20 || detail.Runs[0].ID != "run_05" || detail.Runs[19].ID != "run_24" {
		t.Fatalf("job detail prompt=%q runs=%d first=%q last=%q", detail.Prompt, len(detail.Runs), detail.Runs[0].ID, detail.Runs[19].ID)
	}
	assertNullFailureKeys(t, body)
	getReadAPIBody(t, web.URL+"/api/v1/jobs/missing", workerAuthorization(), http.StatusNotFound)
}

func TestRunDetail(t *testing.T) {
	server, web := newTestHTTPServer(t)
	defer web.Close()
	insertReadAPIJob(t, server, "job_run", "succeeded", "prompt")
	insertReadAPIRun(t, server, "run_detail", "job_run")
	body := getReadAPIBody(t, web.URL+"/api/v1/runs/run_detail", workerAuthorization(), http.StatusOK)
	var detail runDetailResponse
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.JobID != "job_run" || string(detail.Result) != `{"answer":42}` || detail.Events != "event\n" || detail.RenderedPrompt != "rendered secret" {
		t.Fatalf("run detail = %#v", detail)
	}
	assertNullFailureKeys(t, body)
	getReadAPIBody(t, web.URL+"/api/v1/runs/missing", workerAuthorization(), http.StatusNotFound)
}

func TestDetailAuth(t *testing.T) {
	server, web := newTestHTTPServer(t)
	defer web.Close()
	insertReadAPIJob(t, server, "job_auth", "running", "prompt")
	insertReadAPIRun(t, server, "run_auth", "job_auth")
	foreign := map[string]string{"Origin": "https://elsewhere.example", "Sec-Fetch-Site": "cross-site"}
	for _, path := range []string{"/api/v1/jobs/job_auth", "/api/v1/runs/run_auth"} {
		getReadAPIBody(t, web.URL+path, foreign, http.StatusForbidden)
		getReadAPIBody(t, web.URL+path, workerAuthorization(), http.StatusOK)
	}
}

func TestEventsAPI(t *testing.T) {
	server, web := newTestHTTPServer(t)
	defer web.Close()
	if _, err := server.store.db.Exec(`INSERT INTO events(type,subject_kind,subject_id,cause,payload_json,created_at) VALUES
('usage','run','old','completion','{"tokens":1}','2026-09-27T10:00:00Z'),
('cancel','job','new','operator','{"ok":true}','2026-09-27T12:00:00Z'),
('usage','run','newest','completion','{"tokens":2}','2026-09-27T13:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	body := getReadAPIBody(t, web.URL+"/api/v1/events?type=usage&since=2026-09-27T11:00:00Z&limit=1", nil, http.StatusOK)
	var result struct {
		Events []eventResponse `json:"events"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || result.Events[0].SubjectID != "newest" || string(result.Events[0].Payload) != `{"tokens":2}` {
		t.Fatalf("events = %#v", result.Events)
	}
	getReadAPIBody(t, web.URL+"/api/v1/events?since=yesterday", nil, http.StatusBadRequest)
}

func insertReadAPIJob(t *testing.T, server *Server, id, state, prompt string) {
	t.Helper()
	_, err := server.store.db.Exec(`INSERT INTO jobs(id,prompt,repository,command,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, id, prompt, "machinist", "plan", state, readAPITime, readAPITime)
	if err != nil {
		t.Fatal(err)
	}
}

func insertReadAPIRun(t *testing.T, server *Server, id, jobID string) {
	t.Helper()
	_, err := server.store.db.Exec(`INSERT INTO runs(id,job_id,command,command_hash,executor,model,repository,rendered_prompt,timeout_ms,state,result,events) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, jobID, "plan", "hash", "test", "model", "machinist", "rendered secret", 1000, "queued", `{"answer":42}`, "event\n")
	if err != nil {
		t.Fatal(err)
	}
}

func getReadAPIBody(t *testing.T, endpoint string, headers map[string]string, wantStatus int) []byte {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("GET %s status = %d, want %d: %s", endpoint, response.StatusCode, wantStatus, body)
	}
	return body
}

func workerAuthorization() map[string]string {
	return map[string]string{"Authorization": "Bearer secret"}
}

func decodeObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func assertNullFailureKeys(t *testing.T, body []byte) {
	t.Helper()
	text := string(body)
	for _, key := range []string{"failure_class", "reset_at", "reset_source"} {
		if !strings.Contains(text, `"`+key+`":null`) {
			t.Fatalf("%s missing null %s: %s", t.Name(), key, body)
		}
	}
}
