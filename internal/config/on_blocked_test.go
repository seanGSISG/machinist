package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnBlockedGoto(t *testing.T) {
	base := "[commands.plan]\nexecutor='codex'\n[commands.build]\nexecutor='codex'\n[workflows.deliver]\n"
	for _, test := range []struct {
		name, steps, errorText string
	}{
		{"valid", `[{command="plan",id="prepare"},{command="build",on_blocked={goto="prepare",max=2}}]`, ""},
		{"missing max", `[{command="plan"},{command="build",on_blocked={goto="plan"}}]`, "max is required"},
		{"zero max", `[{command="plan"},{command="build",on_blocked={goto="plan",max=0}}]`, "max must be at least 1"},
		{"later step", `[{command="plan",on_blocked={goto="build",max=1}},{command="build"}]`, "must name an earlier step"},
		{"same step", `[{command="plan",on_blocked={goto="plan",max=1}}]`, "must name an earlier step"},
		{"unknown field", `[{command="plan"},{command="build",on_blocked={goto="plan",max=1,limit=2}}]`, "unknown field"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(base+"steps="+test.steps+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadDefinitions(path)
			if test.errorText != "" {
				if err == nil || !strings.Contains(err.Error(), test.errorText) {
					t.Fatalf("error = %v, want text %q", err, test.errorText)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			steps, err := cfg.ResolveTaskWorkflow("deliver", "")
			if err != nil {
				t.Fatal(err)
			}
			if got := steps[1].OnBlocked; got == nil || got.Goto != "prepare" || got.Max != 2 {
				t.Fatalf("on_blocked = %#v", got)
			}
		})
	}
}
