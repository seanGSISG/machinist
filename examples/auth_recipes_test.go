package examples

import (
	"os"
	"path/filepath"
	"strings"
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
	opencode := loaded["opencode"].Connected
	if !opencode.MatchString("┌  Credentials ~/.local/share/opencode/auth.json\n│\n●  OpenAI oauth\n│\n●  GitHub Copilot oauth\n│\n└  2 credentials") {
		t.Error("opencode connected_pattern rejects a listed credential")
	}
	if opencode.MatchString("┌  Credentials ~/.local/share/opencode/auth.json\n│\n└  0 credentials") {
		t.Error("opencode connected_pattern accepts an empty credential list")
	}
	// pi's status stays commented out until the operator picks a provider, so
	// an unconfigured provider never marks pi expired.
	if len(loaded["pi"].Status) != 0 {
		t.Error("pi status must stay opt-in until the provider placeholder is set")
	}
	for _, name := range []string{"codex", "pi"} {
		if !strings.Contains(loaded[name].Path, "node") {
			t.Errorf("%s path must include the node runtime", name)
		}
	}
	if match := loaded["codex"].Code.FindStringSubmatch("Enter this one-time code: ABCD-12345"); match == nil || match[1] != "ABCD-12345" {
		t.Errorf("codex code_pattern matched %v", match)
	}
}

// The shared-login example in docs/connections.md must load as written.
func TestConnectionsDocSharedLoginExampleLoads(t *testing.T) {
	doc, err := os.ReadFile("../docs/connections.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(doc), "## Example: a worker with shared logins")
	if !ok {
		t.Fatal("shared-login example section is missing")
	}
	_, block, ok := strings.Cut(section, "```toml\n")
	if !ok {
		t.Fatal("shared-login example has no toml block")
	}
	block, _, _ = strings.Cut(block, "```")
	base, err := os.ReadFile("worker.toml")
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"claude-opus", "codex-sol", "codex-pool", "pi", "omp", "opencode", "kimi"}
	stubs := ""
	for _, name := range names {
		stubs += "[executors." + name + "]\ncommand = [\"/opt/machinist/bin/agent-run\", \"--\", \"" + name + "\"]\n\n"
	}
	path := filepath.Join(t.TempDir(), "worker.toml")
	if err := os.WriteFile(path, []byte(string(base)+"\n"+stubs+block), 0o600); err != nil {
		t.Fatal(err)
	}
	worker, err := config.LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded := worker.AuthRecipes()
	for _, name := range []string{"claude-opus", "codex-sol", "codex-pool"} {
		if len(loaded[name].Login) != 0 || len(loaded[name].Status) == 0 {
			t.Errorf("%s must share its CLI's login: status only", name)
		}
	}
	if !loaded["pi"].Connected.MatchString(`{"status":"ready","provider":"openai-codex"}`) || loaded["pi"].Connected.MatchString(`{"status":"not_ready","reason":"credentials_not_configured"}`) {
		t.Error("pi connected_pattern")
	}
	for _, name := range []string{"kimi", "omp"} {
		if len(loaded[name].Status) != 0 {
			t.Errorf("%s has no status command", name)
		}
	}
}
