package controlplane

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

func TestControlPlaneIsolatesInvalidConfigEntries(t *testing.T) {
	directory := t.TempDir()
	definitionPath := filepath.Join(directory, "config.toml")
	definition := `[server]
retention_days = 30

[github.repositories]
machinist = "owainlewis/machinist"

[commands.good]
executor = "test"

[commands.bad]
timeout = "1m"

[triggers.interval.bad]
every = "1h"
repository = "machinist"
command = "bad"
prompt = "Run the bad command"
`
	if err := os.WriteFile(definitionPath, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t, filepath.Join(directory, "machinist.db"))
	server, err := NewServer(store, definitionPath, "secret", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(server.triggers) != 0 {
		t.Fatalf("triggers = %#v, want invalid command trigger dropped", server.triggers)
	}
	web := httptest.NewServer(server.Handler())
	t.Cleanup(web.Close)

	status := getConfigIsolationStatus(t, web.URL)
	if len(status.InvalidCommands) != 1 || status.InvalidCommands[0].Name != "bad" {
		t.Fatalf("invalid_commands = %#v", status.InvalidCommands)
	}
	if len(status.Commands) != 1 || status.Commands[0] != "good" {
		t.Fatalf("commands = %#v", status.Commands)
	}

	response, err := web.Client().Get(web.URL + "/api/v1/settings")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/settings status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	putBody := bytes.NewBufferString(`{"base_version":0,"value":{"timeout":"2m"}}`)
	request, err := http.NewRequest(http.MethodPut, web.URL+"/api/v1/settings/commands/good", putBody)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", web.URL)
	request.Header.Set("X-Machinist-CSRF", status.CSRFToken)
	response, err = web.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("PUT setting status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	if err := server.pruneRetention(t.Context(), time.Now().UTC()); err != nil {
		t.Fatalf("prune retention with invalid command: %v", err)
	}

	if err := os.WriteFile(definitionPath, []byte("[commands.good\nexecutor = \"test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status = getConfigIsolationStatus(t, web.URL)
	if len(status.Commands) != 1 || status.Commands[0] != "good" || len(status.InvalidCommands) != 1 || status.InvalidCommands[0].Name != "bad" {
		t.Fatalf("status after syntax error = %#v", status)
	}
	if err := server.pruneRetention(t.Context(), time.Now().UTC()); err != nil {
		t.Fatalf("prune retention after syntax error: %v", err)
	}
}

func getConfigIsolationStatus(t *testing.T, endpoint string) struct {
	Commands        []string                `json:"commands"`
	InvalidCommands []config.InvalidCommand `json:"invalid_commands"`
	CSRFToken       string                  `json:"csrf_token"`
} {
	t.Helper()
	response, err := http.Get(endpoint + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/status status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var status struct {
		Commands        []string                `json:"commands"`
		InvalidCommands []config.InvalidCommand `json:"invalid_commands"`
		CSRFToken       string                  `json:"csrf_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	return status
}
