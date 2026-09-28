package controlplane

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigValidationStatus(t *testing.T) {
	t.Run("healthy configuration reports an empty array", func(t *testing.T) {
		server, web, _ := newConfigValidationServer(t, "[commands.good]\nexecutor = \"test\"\n")
		defer web.Close()

		status := getStatus(t, web.URL)
		if status.InvalidCommands == nil || len(status.InvalidCommands) != 0 {
			t.Fatalf("invalid_commands = %#v, want []", status.InvalidCommands)
		}
		if len(server.triggers) != 0 {
			t.Fatalf("triggers = %#v, want none", server.triggers)
		}
	})

	t.Run("bad startup entry and its trigger are excluded", func(t *testing.T) {
		definition := `[commands.good]
executor = "test"

[commands.bad]
executor = 42

[triggers.interval.bad]
every = "1h"
repository = "machinist"
command = "bad"
prompt = "run"
`
		server, web, _ := newConfigValidationServer(t, definition)
		defer web.Close()

		status := getStatus(t, web.URL)
		if len(status.Commands) != 1 || status.Commands[0] != "good" {
			t.Fatalf("commands = %#v, want [good]", status.Commands)
		}
		if len(status.InvalidCommands) != 1 || status.InvalidCommands[0].Name != "bad" || !strings.Contains(status.InvalidCommands[0].Reason, "config.toml:5:12:") {
			t.Fatalf("invalid_commands = %#v", status.InvalidCommands)
		}
		if len(server.triggers) != 0 {
			t.Fatalf("triggers = %#v, want invalid command trigger excluded", server.triggers)
		}
	})

	t.Run("syntax error reload keeps last good configuration", func(t *testing.T) {
		_, web, path := newConfigValidationServer(t, "[commands.first]\nexecutor = \"test\"\n")
		defer web.Close()

		if err := os.WriteFile(path, []byte("[commands.second]\nexecutor = \"test\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		status := getStatus(t, web.URL)
		if len(status.Commands) != 1 || status.Commands[0] != "second" {
			t.Fatalf("commands after valid reload = %#v, want [second]", status.Commands)
		}

		if err := os.WriteFile(path, []byte("[commands.broken\nexecutor = \"test\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		status = getStatus(t, web.URL)
		if len(status.Commands) != 1 || status.Commands[0] != "second" {
			t.Fatalf("commands after syntax error = %#v, want last good [second]", status.Commands)
		}
		if status.InvalidCommands == nil || len(status.InvalidCommands) != 0 {
			t.Fatalf("invalid_commands after syntax error = %#v, want last good []", status.InvalidCommands)
		}
	})
}

func newConfigValidationServer(t *testing.T, definition string) (*Server, *httptest.Server, string) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(path, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t, filepath.Join(directory, "machinist.db"))
	server, err := NewServer(store, path, "secret", 0)
	if err != nil {
		t.Fatal(err)
	}
	return server, httptest.NewServer(server.Handler()), path
}
