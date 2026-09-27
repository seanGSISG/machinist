package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadWorkerResolvesAuthRecipes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.toml")
	writeTestFile(t, path, `name = "colo"
[executors.claude]
command = ["claude", "--print"]
[executors.claude.auth]
login = ["claude", "auth", "login", "--claudeai"]
status = ["claude", "auth", "status"]
connected_pattern = '"loggedIn":\s*true'
prompt_pattern = '(?i)paste code'
timeout = "5m"
status_interval = "1m"
[executors.pi]
command = ["pi", "-p"]
[executors.pi.auth]
login = ["pi"]
start_input = ["/login\r"]
[executors.plain]
command = ["plain"]
`)
	worker, err := LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	recipes := worker.AuthRecipes()
	if len(recipes) != 2 {
		t.Fatalf("recipes = %v", recipes)
	}
	claude := recipes["claude"]
	if claude.Timeout != 5*time.Minute || claude.StatusInterval != time.Minute || claude.ExpiringWithin != 72*time.Hour {
		t.Fatalf("claude durations = %+v", claude)
	}
	if !claude.Connected.MatchString(`{"loggedIn": true}`) || !claude.Prompt.MatchString("Paste code here") {
		t.Fatal("claude patterns did not compile as written")
	}
	if got := claude.URL.FindString("open https://claude.ai/oauth?x=1 now"); got != "https://claude.ai/oauth?x=1" {
		t.Fatalf("default URL pattern matched %q", got)
	}
	pi := recipes["pi"]
	if pi.Timeout != 10*time.Minute || len(pi.Status) != 0 || pi.StartInput[0] != "/login\r" {
		t.Fatalf("pi recipe = %+v", pi)
	}
}

func TestLoadWorkerRejectsInvalidAuthRecipes(t *testing.T) {
	for name, auth := range map[string]string{
		"empty":             ``,
		"blank executable":  `login = [" "]`,
		"parameter":         `login = ["claude", "{{machinist.model}}"]`,
		"regex":             "login = [\"x\"]\ncode_pattern = \"(\"",
		"expiry group":      "status = [\"x\"]\nexpires_pattern = \"expires .*\"",
		"short timeout":     "login = [\"x\"]\ntimeout = \"1s\"",
		"status interval":   "status = [\"x\"]\nstatus_interval = \"1s\"",
		"orphan status opt": "login = [\"x\"]\nconnected_pattern = \"ok\"",
		"orphan login opt":  "status = [\"x\"]\nprompt_pattern = \"code\"",
		"start input only":  "status = [\"x\"]\nstart_input = [\"/login\\r\"]",
		"unknown field":     "login = [\"x\"]\nenv = [\"A=B\"]",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "worker.toml")
			writeTestFile(t, path, "[executors.claude]\ncommand = [\"claude\"]\n[executors.claude.auth]\n"+auth+"\n")
			if _, err := LoadWorker(path); err == nil || !strings.Contains(err.Error(), "auth") && name != "unknown field" {
				t.Fatalf("LoadWorker error = %v", err)
			}
		})
	}
}
