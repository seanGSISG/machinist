package config

import (
	"os"
	"path/filepath"
	"strings"
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
	wantPosition := path + ":5:12:"
	if !strings.Contains(report.InvalidCommands[0].Reason, wantPosition) {
		t.Fatalf("reason = %q, want %q", report.InvalidCommands[0].Reason, wantPosition)
	}

	if err := os.WriteFile(path, []byte("[commands.bad\nexecutor = \"test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateFile(path); err == nil {
		t.Fatal("ValidateFile accepted invalid TOML syntax")
	}

	t.Run("identical entry bodies retain their own positions", func(t *testing.T) {
		body := "[commands.first]\nexecutor = 42\n\n[commands.second]\nexecutor = 42\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		report, err := ValidateFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(report.InvalidCommands) != 2 {
			t.Fatalf("invalid commands = %#v", report.InvalidCommands)
		}
		if got := report.InvalidCommands[1]; got.Name != "second" || !strings.Contains(got.Reason, path+":5:12:") {
			t.Fatalf("second invalid command = %#v", got)
		}
	})

	t.Run("resolve failures exclude commands and workflows", func(t *testing.T) {
		body := "[commands.good]\nexecutor = \"test\"\n\n[commands.missing_executor]\ntimeout = \"1m\"\n\n[workflows.missing_command]\nsteps = [\"not_defined\"]\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		report, err := ValidateFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Config.Commands) != 1 || len(report.Config.Workflows) != 0 {
			t.Fatalf("loaded config = commands %#v, workflows %#v", report.Config.Commands, report.Config.Workflows)
		}
		if len(report.InvalidCommands) != 2 {
			t.Fatalf("invalid commands = %#v", report.InvalidCommands)
		}
		if got := report.InvalidCommands[0]; got.Name != "missing_executor" || !strings.Contains(got.Reason, path+":5:1:") {
			t.Fatalf("invalid command = %#v", got)
		}
		if got := report.InvalidCommands[1]; got.Name != "missing_command" || !strings.Contains(got.Reason, path+":8:1:") {
			t.Fatalf("invalid workflow = %#v", got)
		}
		loaded, err := LoadDefinitions(path)
		if err != nil {
			t.Fatalf("LoadDefinitions rejected usable configuration: %v", err)
		}
		if len(loaded.Commands) != 1 || len(loaded.Workflows) != 0 {
			t.Fatalf("LoadDefinitions = commands %#v, workflows %#v", loaded.Commands, loaded.Workflows)
		}
	})
}
