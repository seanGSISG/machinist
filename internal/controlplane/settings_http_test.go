package controlplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
)

type settingsClient struct {
	t       *testing.T
	url     string
	headers map[string]string
}

func newSettingsClient(t *testing.T) (*Server, *settingsClient) {
	t.Helper()
	server, web := newTestHTTPServer(t)
	t.Cleanup(web.Close)
	status := getStatus(t, web.URL)
	return server, &settingsClient{t: t, url: web.URL, headers: map[string]string{"Origin": web.URL, "X-Machinist-CSRF": status.CSRFToken}}
}

func (c *settingsClient) do(method, path string, body any, headers map[string]string) (int, map[string]any) {
	c.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	request, err := http.NewRequest(method, c.url+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	decoded := map[string]any{}
	_ = json.NewDecoder(response.Body).Decode(&decoded)
	return response.StatusCode, decoded
}

func (c *settingsClient) put(kind, name string, base int64, value any) (int, map[string]any) {
	c.t.Helper()
	return c.do(http.MethodPut, "/api/v1/settings/"+kind+"/"+name, map[string]any{"base_version": base, "value": value}, c.headers)
}

func (c *settingsClient) mustPut(kind, name string, base int64, value any) int64 {
	c.t.Helper()
	code, body := c.put(kind, name, base, value)
	if code != http.StatusOK {
		c.t.Fatalf("put %s/%s = %d %v", kind, name, code, body)
	}
	return int64(body["version"].(map[string]any)["id"].(float64))
}

func (c *settingsClient) submit(input map[string]string) string {
	c.t.Helper()
	code, body := c.do(http.MethodPost, "/api/v1/jobs", input, c.headers)
	if code != http.StatusCreated {
		c.t.Fatalf("submit = %d %v", code, body)
	}
	return body["id"].(string)
}

func registerSettingsWorker(t *testing.T, server *Server) {
	t.Helper()
	request := protocol.PollRequest{InstanceID: "worker-a", Name: "colo", Executors: []string{"test", "codex", "script"}, Repositories: []string{"machinist"},
		Models: map[string][]string{"codex": {"luna", "sol"}, "test": {}}}
	if _, err := server.store.Poll(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func runSnapshot(t *testing.T, server *Server, jobID string) (executor, model, prompt string, timeoutMillis int64) {
	t.Helper()
	if err := server.store.db.QueryRowContext(t.Context(), `SELECT executor,model,rendered_prompt,timeout_ms FROM runs WHERE job_id=? ORDER BY rowid LIMIT 1`, jobID).Scan(&executor, &model, &prompt, &timeoutMillis); err != nil {
		t.Fatal(err)
	}
	return
}

func TestSettingsRequireSubmissionAuthorization(t *testing.T) {
	server, client := newSettingsClient(t)
	registerSettingsWorker(t, server)
	value := map[string]any{"base_version": 0, "value": map[string]string{"timeout": "2m"}}
	if code, _ := client.do(http.MethodPut, "/api/v1/settings/commands/plan", value, nil); code != http.StatusForbidden {
		t.Fatalf("unauthenticated put = %d", code)
	}
	foreign := map[string]string{"Origin": "https://evil.example", "X-Machinist-CSRF": client.headers["X-Machinist-CSRF"]}
	if code, _ := client.do(http.MethodPut, "/api/v1/settings/commands/plan", value, foreign); code != http.StatusForbidden {
		t.Fatalf("foreign origin put = %d", code)
	}
	if code, _ := client.do(http.MethodPost, "/api/v1/settings/versions/1/revert", nil, nil); code != http.StatusForbidden {
		t.Fatalf("unauthenticated revert = %d", code)
	}
	if code, _ := client.do(http.MethodPut, "/api/v1/settings/commands/plan", value, map[string]string{"Authorization": "Bearer wrong"}); code != http.StatusUnauthorized {
		t.Fatalf("wrong bearer = %d", code)
	}
	if code, body := client.do(http.MethodPut, "/api/v1/settings/commands/plan", value, map[string]string{"Authorization": "Bearer secret"}); code != http.StatusOK {
		t.Fatalf("bearer put = %d %v", code, body)
	}
}

func TestSettingsCommandOverrideAppliesOnlyToNewTasks(t *testing.T) {
	server, client := newSettingsClient(t)
	registerSettingsWorker(t, server)
	before := client.submit(map[string]string{"command": "plan", "repository": "machinist", "prompt": "first"})

	if code, body := client.put("commands", "plan", 0, map[string]string{"executor": "ghost"}); code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(body["error"]), "not advertised") {
		t.Fatalf("unadvertised executor = %d %v", code, body)
	}
	if code, body := client.put("commands", "plan", 0, map[string]string{"executor": "codex", "model": "opus"}); code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(body["error"]), "model") {
		t.Fatalf("unknown alias = %d %v", code, body)
	}
	if code, _ := client.put("commands", "plan", 0, map[string]string{"executor": "codex", "command": "rm -rf /"}); code != http.StatusBadRequest {
		t.Fatalf("unknown field must be rejected, got %d", code)
	}
	version := client.mustPut("commands", "plan", 0, map[string]string{"executor": "codex", "model": "sol", "timeout": "2m", "prompt": "Revised:\n{{machinist.prompt}}"})
	if code, _ := client.put("commands", "plan", 0, map[string]string{"timeout": "3m"}); code != http.StatusConflict {
		t.Fatalf("stale base version = %d", code)
	}

	after := client.submit(map[string]string{"command": "plan", "repository": "machinist", "prompt": "second"})
	executor, model, prompt, timeout := runSnapshot(t, server, after)
	if executor != "codex" || model != "sol" || prompt != "Revised:\nsecond" || timeout != 120000 {
		t.Fatalf("new task snapshot = %q %q %q %d", executor, model, prompt, timeout)
	}
	executor, model, prompt, timeout = runSnapshot(t, server, before)
	if executor != "test" || model != "" || !strings.HasPrefix(prompt, "Plan this request:") || timeout != 60000 {
		t.Fatalf("submitted task changed: %q %q %q %d", executor, model, prompt, timeout)
	}
	explicit := client.submit(map[string]string{"command": "plan", "repository": "machinist", "prompt": "third", "model": "luna"})
	if _, model, _, _ = runSnapshot(t, server, explicit); model != "luna" {
		t.Fatalf("explicit model = %q", model)
	}

	client.mustPut("commands", "plan", version, nil)
	cleared := client.submit(map[string]string{"command": "plan", "repository": "machinist", "prompt": "fourth"})
	if executor, _, _, _ = runSnapshot(t, server, cleared); executor != "test" {
		t.Fatalf("clearing the override should restore config.toml, got %q", executor)
	}
}

func TestSettingsHistoryAndRevert(t *testing.T) {
	server, client := newSettingsClient(t)
	registerSettingsWorker(t, server)
	first := client.mustPut("commands", "plan", 0, map[string]string{"timeout": "2m"})
	second := client.mustPut("commands", "plan", first, map[string]string{"timeout": "5m"})
	code, body := client.do(http.MethodGet, "/api/v1/settings/commands/plan/history", nil, nil)
	versions := body["versions"].([]any)
	if code != http.StatusOK || len(versions) != 2 || int64(versions[0].(map[string]any)["id"].(float64)) != second {
		t.Fatalf("history = %d %v", code, body)
	}
	code, body = client.do(http.MethodPost, fmt.Sprintf("/api/v1/settings/versions/%d/revert", first), nil, client.headers)
	if code != http.StatusOK {
		t.Fatalf("revert = %d %v", code, body)
	}
	reverted := body["version"].(map[string]any)
	if int64(reverted["reverted_from"].(float64)) != first {
		t.Fatalf("revert metadata = %v", reverted)
	}
	job := client.submit(map[string]string{"command": "plan", "repository": "machinist", "prompt": "x"})
	if _, _, _, timeout := runSnapshot(t, server, job); timeout != 120000 {
		t.Fatalf("reverted timeout = %d", timeout)
	}
	if code, _ = client.do(http.MethodPost, "/api/v1/settings/versions/999/revert", nil, client.headers); code != http.StatusNotFound {
		t.Fatalf("missing version = %d", code)
	}
}

func TestSettingsExecutorDefaultAndUIWorkflow(t *testing.T) {
	server, client := newSettingsClient(t)
	registerSettingsWorker(t, server)
	if code, _ := client.put("executors", "codex", 0, map[string]string{"default_model": "opus"}); code != http.StatusBadRequest {
		t.Fatalf("unknown default alias = %d", code)
	}
	if code, _ := client.put("executors", "script", 0, map[string]string{"default_model": "any"}); code != http.StatusBadRequest {
		t.Fatalf("executor without model support = %d", code)
	}
	client.mustPut("executors", "codex", 0, map[string]string{"default_model": "luna"})
	client.mustPut("commands", "build", 0, map[string]string{"executor": "codex", "prompt": "Build {{task.spec}} into {{task.output_dir}}"})
	client.mustPut("workflows", "ship", 0, map[string]any{"steps": []map[string]any{{"command": "plan"}, {"command": "build", "approval": true}}})

	status := getStatus(t, client.url)
	if !strings.Contains(strings.Join(status.Workflows, ","), "ship") || !strings.Contains(strings.Join(status.Commands, ","), "build") {
		t.Fatalf("status = %v %v", status.Workflows, status.Commands)
	}
	job := client.submit(map[string]string{"workflow": "ship", "repository": "machinist", "spec": "add settings"})
	var plan string
	if err := server.store.db.QueryRowContext(t.Context(), `SELECT plan FROM workflow_jobs WHERE job_id=?`, job).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, `"Executor":"codex","Command":null,"Model":"luna"`) {
		t.Fatalf("workflow plan did not use the executor default: %s", plan)
	}

	// Removing a command that a stored workflow uses is rejected.
	history, err := server.store.SettingHistory(t.Context(), "commands", "build")
	if err != nil {
		t.Fatal(err)
	}
	code, body := client.put("commands", "build", history[0].ID, nil)
	if code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(body["error"]), "ship") {
		t.Fatalf("breaking delete = %d %v", code, body)
	}
	if code, body = client.put("workflows", "empty", 0, map[string]any{"steps": []any{}}); code != http.StatusBadRequest {
		t.Fatalf("empty workflow = %d %v", code, body)
	}
}

func TestSettingsReportPromptWarningsAndStaleOverrides(t *testing.T) {
	server, client := newSettingsClient(t)
	registerSettingsWorker(t, server)
	large := strings.Repeat("Follow this instruction carefully.\n", 70) + "{{machinist.prompt}}"
	code, body := client.put("commands", "plan", 0, map[string]string{"prompt": large, "model": "anything"})
	if code != http.StatusOK || len(body["warnings"].([]any)) != 2 {
		t.Fatalf("warnings = %d %v", code, body)
	}
	if err := os.WriteFile(server.definitionPath, []byte("[commands.other]\nexecutor = \"test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status := getStatus(t, client.url)
	if len(status.Commands) != 1 || status.Commands[0] != "other" {
		t.Fatalf("a stale override must not break status: %v", status.Commands)
	}
	code, body = client.do(http.MethodGet, "/api/v1/settings", nil, nil)
	problems := body["problems"].([]any)
	if code != http.StatusOK || len(problems) != 1 || problems[0].(map[string]any)["name"] != "plan" {
		t.Fatalf("settings = %d %v", code, body)
	}
	executors := body["executors"].([]any)
	if len(executors) < 3 {
		t.Fatalf("executors = %v", executors)
	}
}

func TestSettingsTablesKeepSchemaVersionForRollback(t *testing.T) {
	server, client := newSettingsClient(t)
	registerSettingsWorker(t, server)
	client.mustPut("commands", "plan", 0, map[string]string{"timeout": "2m"})
	var version int
	if err := server.store.db.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 7 {
		t.Fatalf("settings must stay additive so older releases can open the database; user_version = %d", version)
	}
}

func TestSettingsSkipOverridesWorkersNoLongerServe(t *testing.T) {
	server, client := newSettingsClient(t)
	registerSettingsWorker(t, server)
	client.mustPut("commands", "plan", 0, map[string]string{"executor": "codex", "model": "sol"})
	client.mustPut("executors", "codex", 0, map[string]string{"default_model": "sol"})

	// The worker drops model "sol": the stored overrides can no longer be claimed.
	request := protocol.PollRequest{InstanceID: "worker-a", Name: "colo", Executors: []string{"test", "codex", "script"}, Repositories: []string{"machinist"},
		Models: map[string][]string{"codex": {"luna"}, "test": {}}}
	if _, err := server.store.Poll(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	job := client.submit(map[string]string{"command": "plan", "repository": "machinist", "prompt": "later"})
	if executor, model, _, _ := runSnapshot(t, server, job); executor != "test" || model != "" {
		t.Fatalf("unservable override must be skipped, got %q %q", executor, model)
	}
	code, body := client.do(http.MethodGet, "/api/v1/settings", nil, nil)
	problems := fmt.Sprint(body["problems"])
	if code != http.StatusOK || !strings.Contains(problems, "plan") || !strings.Contains(problems, "codex") {
		t.Fatalf("skipped overrides must be reported: %d %v", code, body["problems"])
	}
}

func TestTrustedOriginsAllowAProxiedUI(t *testing.T) {
	server, client := newSettingsClient(t)
	registerSettingsWorker(t, server)
	value := map[string]any{"base_version": 0, "value": map[string]string{"timeout": "2m"}}
	proxied := map[string]string{"Origin": "https://machinist.lab.example", "X-Machinist-CSRF": client.headers["X-Machinist-CSRF"]}
	if code, _ := client.do(http.MethodPut, "/api/v1/settings/commands/plan", value, proxied); code != http.StatusForbidden {
		t.Fatalf("untrusted https origin = %d, want 403", code)
	}
	server.TrustOrigins([]string{"https://machinist.lab.example"})
	if code, body := client.do(http.MethodPut, "/api/v1/settings/commands/plan", value, proxied); code != http.StatusOK {
		t.Fatalf("trusted origin = %d %v", code, body)
	}
	noCSRF := map[string]string{"Origin": "https://machinist.lab.example"}
	if code, _ := client.do(http.MethodPut, "/api/v1/settings/commands/plan", value, noCSRF); code != http.StatusForbidden {
		t.Fatalf("trusted origin without CSRF = %d, want 403", code)
	}
	evil := map[string]string{"Origin": "https://machinist.lab.example.evil.com", "X-Machinist-CSRF": client.headers["X-Machinist-CSRF"]}
	if code, _ := client.do(http.MethodPut, "/api/v1/settings/commands/plan", value, evil); code != http.StatusForbidden {
		t.Fatalf("look-alike origin = %d, want 403", code)
	}
}
