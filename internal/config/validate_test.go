package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestPerEntryValidation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.toml")
	body := "[commands.good]\nexecutor = \"test\"\n\n[commands.bad]\nexecutor = 42\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := ValidateFile(path)
	if err != nil {
		t.Fatalf("ValidateFile: %v", err)
	}
	if _, ok := report.Config.Commands["good"]; !ok {
		t.Fatalf("good command was not loaded: %#v", report.Config.Commands)
	}
	if _, ok := report.Config.Commands["bad"]; ok {
		t.Fatalf("bad command was loaded: %#v", report.Config.Commands)
	}
	if len(report.InvalidCommands) != 1 || report.InvalidCommands[0].Name != "bad" {
		t.Fatalf("invalid commands = %#v", report.InvalidCommands)
	}
	wantPosition := regexp.MustCompile(regexp.QuoteMeta(path) + `:5:\d+:`)
	if !wantPosition.MatchString(report.InvalidCommands[0].Reason) {
		t.Fatalf("reason = %q, want file:line:col", report.InvalidCommands[0].Reason)
	}

	if err := os.WriteFile(path, []byte("[commands.bad\nexecutor = \"test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateFile(path); err == nil {
		t.Fatal("ValidateFile accepted invalid TOML syntax")
	}
}
