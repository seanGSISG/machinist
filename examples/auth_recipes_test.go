package examples

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
)

// The documented recipes must load as written on top of the example worker.
func TestAuthRecipesLoadWithExampleWorker(t *testing.T) {
	base, err := os.ReadFile("worker.toml")
	if err != nil {
		t.Fatal(err)
	}
	recipes, err := os.ReadFile("auth-recipes.toml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "worker.toml")
	if err := os.WriteFile(path, append(append(base, '\n'), recipes...), 0o600); err != nil {
		t.Fatal(err)
	}
	worker, err := config.LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded := worker.AuthRecipes()
	for _, name := range []string{"claude", "codex", "kimi", "omp", "opencode", "pi"} {
		if len(loaded[name].Login) == 0 {
			t.Errorf("%s has no login recipe", name)
		}
	}
	if !loaded["claude"].Connected.MatchString(`{"loggedIn": true, "authMethod": "claude.ai"}`) || loaded["claude"].Connected.MatchString(`{"loggedIn": false}`) {
		t.Error("claude connected_pattern")
	}
	if !loaded["codex"].Connected.MatchString("Logged in using ChatGPT") || loaded["codex"].Connected.MatchString("Not logged in") {
		t.Error("codex connected_pattern")
	}
	if match := loaded["codex"].Code.FindStringSubmatch("Enter this one-time code: ABCD-12345"); match == nil || match[1] != "ABCD-12345" {
		t.Errorf("codex code_pattern matched %v", match)
	}
}
