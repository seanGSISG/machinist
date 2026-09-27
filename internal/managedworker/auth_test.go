package managedworker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/controlplane"
	"github.com/owainlewis/machinist/internal/protocol"
)

// TestConnectFromTheWebAPIRunsTheWorkerRecipe drives a whole login through the
// control plane HTTP API against a real worker auth broker and a fake CLI.
func TestConnectFromTheWebAPIRunsTheWorkerRecipe(t *testing.T) {
	directory := t.TempDir()
	definitionPath := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(definitionPath, []byte("[commands.plan]\nexecutor=\"fake\"\ntimeout=\"5s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := controlplane.OpenStore(filepath.Join(directory, "machinist.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, err := controlplane.NewServer(store, definitionPath, "secret", 0)
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(server.Handler())
	defer web.Close()
	tokenPath := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenPath, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	writeFakeCLI(t, bin, "fake-login", fakeLoginScript)
	writeFakeCLI(t, bin, "fake-status", fakeStatusScript)
	workerConfig := config.Worker{
		Name:         "colo",
		ControlPlane: config.ControlPlane{URL: web.URL, TokenFile: tokenPath},
		Executors: map[string]config.Executor{"fake": {Command: []string{"fake"}, Auth: &config.ExecutorAuth{
			Login: []string{"fake-login"}, Status: []string{"fake-status"}, Path: bin + ":/usr/bin:/bin",
			ConnectedPattern: `"loggedIn": true`, CodePattern: `device code: ([A-Z0-9-]+)`, PromptPattern: `Paste code here`,
		}}},
	}
	client, err := NewClient(workerConfig)
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedWriter{}
	agent, err := newAuthAgent(workerConfig, client, "worker_test", logs)
	if err != nil {
		t.Fatal(err)
	}
	agent.environment = testLoginEnvironment(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { agent.run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	connection := func() map[string]any {
		var body struct {
			Connections []map[string]any `json:"connections"`
		}
		request(t, http.MethodGet, web.URL+"/api/v1/connections", nil, nil, &body)
		if len(body.Connections) != 1 {
			return nil
		}
		return body.Connections[0]
	}
	eventually(t, "status check reports expired", func() bool { c := connection(); return c != nil && c["state"] == "expired" })

	var status struct {
		CSRFToken string `json:"csrf_token"`
	}
	request(t, http.MethodGet, web.URL+"/api/v1/status", nil, nil, &status)
	headers := map[string]string{"Origin": web.URL, "X-Machinist-CSRF": status.CSRFToken}
	var started struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		Token string `json:"token"`
	}
	if code := request(t, http.MethodPost, web.URL+"/api/v1/connections/colo/fake/login", map[string]any{}, headers, &started); code != http.StatusCreated {
		t.Fatalf("login = %d", code)
	}
	headers["X-Machinist-Login-Token"] = started.Token
	sessionURL := web.URL + "/api/v1/connections/sessions/" + started.Session.ID
	var session map[string]any
	eventually(t, "login shows the link, code and prompt", func() bool {
		session = map[string]any{}
		request(t, http.MethodGet, sessionURL, nil, headers, &session)
		return session["awaiting_input"] == true && session["code"] == "WXYZ-1234"
	})
	if session["url"] != "https://login.example.test/oauth?state=abc123" || session["state"] != "awaiting_input" {
		t.Fatalf("session = %v", session)
	}
	if code := request(t, http.MethodPost, sessionURL+"/input", map[string]string{"text": "sekrit-paste-value"}, headers, nil); code != http.StatusNoContent {
		t.Fatalf("input = %d", code)
	}
	eventually(t, "login succeeds", func() bool {
		session = map[string]any{}
		request(t, http.MethodGet, sessionURL, nil, headers, &session)
		return session["state"] == "succeeded"
	})
	if transcript, _ := session["transcript"].(string); strings.Contains(transcript, "sekrit-paste-value") || !strings.Contains(transcript, "Logged in") {
		t.Fatalf("transcript = %q", transcript)
	}
	eventually(t, "status is re-checked after login", func() bool { c := connection(); return c != nil && c["state"] == "connected" })
	if strings.Contains(logs.String(), "sekrit") || strings.Contains(logs.String(), "WXYZ") || strings.Contains(logs.String(), "https://") {
		t.Fatalf("worker logs leak login details:\n%s", logs.String())
	}
}

func TestAuthAgentNeverRunsArgvFromTheControlPlane(t *testing.T) {
	agent := &authAgent{recipes: map[string]config.AuthRecipe{"status-only": {Executor: "status-only", Status: []string{"true"}}}, sessions: map[string]*loginSession{}, wake: make(chan struct{}, 1), stderr: io.Discard}
	for _, action := range []protocol.LoginAction{
		{SessionID: "login_0123456789abcdef", Kind: protocol.LoginActionStart, Executor: "rm -rf /"},
		{SessionID: "login_fedcba9876543210", Kind: protocol.LoginActionStart, Executor: "status-only"},
		{SessionID: "../../etc", Kind: protocol.LoginActionStart, Executor: "status-only"},
	} {
		agent.handle(context.Background(), action)
	}
	if len(agent.sessions) != 2 {
		t.Fatalf("sessions = %v", agent.sessions)
	}
	for _, session := range agent.sessions {
		report, _, _ := session.report()
		if report.State != protocol.LoginFailed || !strings.Contains(report.Error, "no login recipe") {
			t.Fatalf("report = %+v", report)
		}
	}
}

type lockedWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (w *lockedWriter) Write(chunk []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(chunk)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func request(t *testing.T, method, endpoint string, body any, headers map[string]string, output any) int {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	httpRequest, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		httpRequest.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if output != nil && response.StatusCode < 300 {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
	return response.StatusCode
}

func eventually(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", description)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
