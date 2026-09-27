package controlplane

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

var workerAuth = map[string]string{"Authorization": "Bearer secret"}

func authSync(t *testing.T, c *settingsClient, request protocol.AuthSyncRequest) []protocol.LoginAction {
	t.Helper()
	code, body := c.do(http.MethodPost, "/api/v1/workers/auth", request, workerAuth)
	if code != http.StatusOK {
		t.Fatalf("auth sync = %d %v", code, body)
	}
	actions := []protocol.LoginAction{}
	raw, _ := body["actions"].([]any)
	for _, item := range raw {
		action := item.(map[string]any)
		text, _ := action["text"].(string)
		executor, _ := action["executor"].(string)
		actions = append(actions, protocol.LoginAction{SessionID: action["session_id"].(string), Kind: action["kind"].(string), Executor: executor, Text: text})
	}
	return actions
}

func claudeReport(state string) protocol.AuthSyncRequest {
	return protocol.AuthSyncRequest{InstanceID: "worker-a", Name: "colo", Executors: map[string]protocol.ExecutorAuthReport{
		"claude": {Login: true, LoginTimeout: time.Minute.Milliseconds(), State: state},
		"codex":  {State: protocol.AuthConnected},
	}}
}

func TestConnectionsLoginFlowIsBoundToTheRequestingSession(t *testing.T) {
	_, client := newSettingsClient(t)
	authSync(t, client, claudeReport(protocol.AuthExpired))

	code, body := client.do(http.MethodGet, "/api/v1/connections", nil, nil)
	connections := body["connections"].([]any)
	if code != http.StatusOK || len(connections) != 2 || connections[0].(map[string]any)["state"] != "expired" || connections[0].(map[string]any)["online"] != true {
		t.Fatalf("connections = %d %v", code, body)
	}
	if code, _ := client.do(http.MethodPost, "/api/v1/connections/colo/claude/login", map[string]any{}, nil); code != http.StatusForbidden {
		t.Fatalf("login without CSRF = %d", code)
	}
	if code, _ := client.do(http.MethodPost, "/api/v1/connections/colo/claude/login", map[string]any{}, map[string]string{"Origin": "http://evil.example", "X-Machinist-CSRF": client.headers["X-Machinist-CSRF"]}); code != http.StatusForbidden {
		t.Fatalf("login from foreign origin = %d", code)
	}
	if code, _ := client.do(http.MethodPost, "/api/v1/connections/colo/codex/login", map[string]any{}, client.headers); code != http.StatusNotFound {
		t.Fatalf("login without recipe = %d", code)
	}
	code, body = client.do(http.MethodPost, "/api/v1/connections/colo/claude/login", map[string]any{}, client.headers)
	if code != http.StatusCreated {
		t.Fatalf("login = %d %v", code, body)
	}
	token := body["token"].(string)
	id := body["session"].(map[string]any)["id"].(string)
	if code, _ := client.do(http.MethodPost, "/api/v1/connections/colo/claude/login", map[string]any{}, client.headers); code != http.StatusConflict {
		t.Fatalf("second login = %d", code)
	}

	owner := map[string]string{"Origin": client.headers["Origin"], "X-Machinist-CSRF": client.headers["X-Machinist-CSRF"], loginTokenHeader: token}
	stranger := map[string]string{"Origin": client.headers["Origin"], "X-Machinist-CSRF": client.headers["X-Machinist-CSRF"], loginTokenHeader: "logintoken_guess"}
	if code, _ := client.do(http.MethodGet, "/api/v1/connections/sessions/"+id, nil, stranger); code != http.StatusNotFound {
		t.Fatalf("session read with wrong token = %d", code)
	}
	if code, _ := client.do(http.MethodPost, "/api/v1/connections/sessions/"+id+"/input", map[string]string{"text": "x"}, owner); code != http.StatusBadRequest {
		t.Fatalf("input before the worker started = %d", code)
	}

	actions := authSync(t, client, claudeReport(protocol.AuthExpired))
	if len(actions) != 1 || actions[0].Kind != protocol.LoginActionStart || actions[0].Executor != "claude" || actions[0].SessionID != id {
		t.Fatalf("start actions = %+v", actions)
	}
	report := claudeReport(protocol.AuthExpired)
	report.Active = []string{id}
	report.Sessions = []protocol.LoginSessionReport{{ID: id, State: protocol.LoginAwaiting, AwaitingInput: true, URL: "https://claude.example.test/oauth?x=1", Code: "ABCD-1234", Transcript: "Paste code here"}}
	if actions := authSync(t, client, report); len(actions) != 0 {
		t.Fatalf("unexpected actions = %+v", actions)
	}
	code, body = client.do(http.MethodGet, "/api/v1/connections/sessions/"+id, nil, owner)
	if code != http.StatusOK || body["url"] != "https://claude.example.test/oauth?x=1" || body["code"] != "ABCD-1234" || body["awaiting_input"] != true {
		t.Fatalf("session = %d %v", code, body)
	}
	if code, _ := client.do(http.MethodPost, "/api/v1/connections/sessions/"+id+"/input", map[string]string{"text": "pasted-code"}, stranger); code != http.StatusNotFound {
		t.Fatalf("input with wrong token = %d", code)
	}
	if code, _ := client.do(http.MethodPost, "/api/v1/connections/sessions/"+id+"/input", map[string]string{"text": "pasted-code"}, owner); code != http.StatusNoContent {
		t.Fatalf("input = %d", code)
	}
	actions = authSync(t, client, protocol.AuthSyncRequest{InstanceID: "worker-b", Name: "other", Executors: map[string]protocol.ExecutorAuthReport{}})
	if len(actions) != 0 {
		t.Fatalf("another worker received actions: %+v", actions)
	}
	report.Sessions = nil
	actions = authSync(t, client, report)
	if len(actions) != 1 || actions[0].Kind != protocol.LoginActionInput || actions[0].Text != "pasted-code" {
		t.Fatalf("input actions = %+v", actions)
	}
	if actions := authSync(t, client, report); len(actions) != 0 {
		t.Fatalf("input was delivered twice: %+v", actions)
	}

	done := claudeReport(protocol.AuthConnected)
	done.Sessions = []protocol.LoginSessionReport{{ID: id, State: protocol.LoginSucceeded}}
	authSync(t, client, done)
	code, body = client.do(http.MethodGet, "/api/v1/connections/sessions/"+id, nil, owner)
	if code != http.StatusOK || body["state"] != "succeeded" {
		t.Fatalf("finished session = %d %v", code, body)
	}
	if code, _ := client.do(http.MethodPost, "/api/v1/connections/sessions/"+id+"/input", map[string]string{"text": "again"}, owner); code != http.StatusConflict {
		t.Fatalf("input after the session ended = %d", code)
	}
	if code, _ := client.do(http.MethodPost, "/api/v1/connections/sessions/"+id+"/cancel", nil, owner); code != http.StatusConflict {
		t.Fatalf("cancel after the session ended = %d", code)
	}
	status := getStatus(t, client.url)
	if len(status.Connections) != 2 || status.Connections[0].State != protocol.AuthConnected {
		t.Fatalf("status connections = %+v", status.Connections)
	}
}

func TestConnectionsRejectOfflineWorkersAndReplaceStuckSessions(t *testing.T) {
	server, client := newSettingsClient(t)
	authSync(t, client, claudeReport(protocol.AuthUnknown))
	code, body := client.do(http.MethodPost, "/api/v1/connections/colo/claude/login", map[string]any{}, client.headers)
	if code != http.StatusCreated {
		t.Fatalf("login = %d %v", code, body)
	}
	first := body["session"].(map[string]any)["id"].(string)
	code, body = client.do(http.MethodPost, "/api/v1/connections/colo/claude/login", map[string]any{"replace": true}, client.headers)
	if code != http.StatusCreated {
		t.Fatalf("replacing login = %d %v", code, body)
	}
	second := body["session"].(map[string]any)["id"].(string)
	actions := authSync(t, client, claudeReport(protocol.AuthUnknown))
	if len(actions) != 1 || actions[0].SessionID != second || actions[0].Kind != protocol.LoginActionStart {
		t.Fatalf("replaced session still starts: %+v (first %s)", actions, first)
	}
	later := time.Now().Add(time.Minute)
	server.store.now = func() time.Time { return later }
	if code, body := client.do(http.MethodPost, "/api/v1/connections/colo/claude/login", map[string]any{"replace": true}, client.headers); code != http.StatusConflict || !strings.Contains(body["error"].(string), "offline") {
		t.Fatalf("login on offline worker = %d %v", code, body)
	}
}

// During a restart two instances can share a worker name. A login goes to
// the instance that reported last, and the other one cannot take it over.
func TestLoginIsBoundToOneWorkerInstance(t *testing.T) {
	server, client := newSettingsClient(t)
	authSync(t, client, claudeReport(protocol.AuthExpired))
	later := time.Now().Add(time.Second)
	server.store.now = func() time.Time { return later }
	newer := claudeReport(protocol.AuthExpired)
	newer.InstanceID = "worker-a2"
	authSync(t, client, newer)
	code, body := client.do(http.MethodPost, "/api/v1/connections/colo/claude/login", map[string]any{}, client.headers)
	if code != http.StatusCreated {
		t.Fatalf("login = %d %v", code, body)
	}
	id := body["session"].(map[string]any)["id"].(string)
	token := body["token"].(string)

	old := claudeReport(protocol.AuthExpired)
	old.Active = []string{id}
	old.Sessions = []protocol.LoginSessionReport{{ID: id, State: protocol.LoginSucceeded}}
	if actions := authSync(t, client, old); len(actions) != 0 {
		t.Fatalf("the older instance took the login: %+v", actions)
	}
	owner := map[string]string{"Origin": client.headers["Origin"], "X-Machinist-CSRF": client.headers["X-Machinist-CSRF"], loginTokenHeader: token}
	if code, body := client.do(http.MethodGet, "/api/v1/connections/sessions/"+id, nil, owner); code != http.StatusOK || body["state"] != protocol.LoginPending {
		t.Fatalf("the older instance changed the session: %d %v", code, body)
	}
	if actions := authSync(t, client, newer); len(actions) != 1 || actions[0].SessionID != id || actions[0].Kind != protocol.LoginActionStart {
		t.Fatalf("the newer instance did not get the login: %+v", actions)
	}
}

func TestTruncateUTF8KeepsWholeRunes(t *testing.T) {
	if got := truncateUTF8("abé", 3); got != "ab" {
		t.Fatalf("truncateUTF8 = %q", got)
	}
	if got := truncateUTF8("abc", 3); got != "abc" {
		t.Fatalf("truncateUTF8 = %q", got)
	}
}

func TestLoginBrokerExpiresAndSanitizesSessions(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	broker := newLoginBroker(func() time.Time { return now })
	view, token, err := broker.start("colo", "worker-a", "claude", time.Minute, false)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(loginPickupWindow + time.Second)
	if view, _ = broker.get(view.ID, token); view.State != protocol.LoginFailed {
		t.Fatalf("unclaimed session = %+v", view)
	}

	view, token, _ = broker.start("colo", "worker-a", "claude", time.Minute, false)
	broker.sync("worker-a", nil, nil)
	broker.sync("worker-a", []protocol.LoginSessionReport{{ID: view.ID, State: protocol.LoginRunning, URL: "javascript:alert(1)", Code: strings.Repeat("x", 100)}}, []string{view.ID})
	if view, _ = broker.get(view.ID, token); view.URL != "" || view.Code != "" || view.State != protocol.LoginRunning {
		t.Fatalf("unsafe report was kept: %+v", view)
	}
	now = now.Add(loginWorkerSilence + time.Second)
	if view, _ = broker.get(view.ID, token); view.State != protocol.LoginFailed || !strings.Contains(view.Error, "stopped responding") {
		t.Fatalf("silent worker session = %+v", view)
	}

	view, token, _ = broker.start("colo", "worker-a", "claude", time.Minute, false)
	broker.sync("worker-a", nil, nil)
	if err := broker.input(view.ID, token, "", "enter"); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.cancel(view.ID, token); err != nil {
		t.Fatal(err)
	}
	actions := broker.sync("worker-a", nil, nil)
	if len(actions) != 1 || actions[0].Kind != protocol.LoginActionCancel {
		t.Fatalf("cancel should drop queued input and send cancel: %+v", actions)
	}
	if err := broker.input(view.ID, token, "", "delete-everything"); err != ErrLoginEnded {
		t.Fatalf("input after cancel = %v", err)
	}
	now = now.Add(loginRetention + time.Second)
	if _, err := broker.get(view.ID, token); err != ErrLoginNotFound {
		t.Fatalf("finished session was retained: %v", err)
	}
}

func TestExpiredExecutorIsNotLeased(t *testing.T) {
	store := openTestStore(t, t.TempDir()+"/machinist.db")
	agent := config.ResolvedCommand{Name: "plan", Executor: "claude", Prompt: "Plan", Timeout: time.Minute, Hash: "plan-hash"}
	if _, err := store.CreateJob(t.Context(), "Plan", "machinist", "plan", agent); err != nil {
		t.Fatal(err)
	}
	poll := protocol.PollRequest{InstanceID: "worker-a", Name: "colo", Executors: []string{"claude"}, Repositories: []string{"machinist"}}
	report := claudeReport(protocol.AuthExpired)
	if err := store.RecordExecutorAuth(t.Context(), report); err != nil {
		t.Fatal(err)
	}
	if run, err := store.Poll(t.Context(), poll); err != nil || run != nil {
		t.Fatalf("expired executor leased = %+v, %v", run, err)
	}
	report.Executors["claude"] = protocol.ExecutorAuthReport{Login: true, State: protocol.AuthExpiring}
	if err := store.RecordExecutorAuth(t.Context(), report); err != nil {
		t.Fatal(err)
	}
	if run, err := store.Poll(t.Context(), poll); err != nil || run == nil {
		t.Fatalf("expiring executor was not leased: %v", err)
	}
}
