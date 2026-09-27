package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSettingsFixture(t *testing.T) Config {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "plan.md"), []byte("Plan {{task.spec}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "config.toml")
	body := "[commands.plan]\nexecutor = \"claude\"\nprompt_file = \"plan.md\"\ntimeout = \"10m\"\n" +
		"[commands.build]\nexecutor = \"codex\"\n" +
		"[workflows.deliver]\nsteps = [\"plan\", { command = \"build\", approval = \"before\" }]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	definition, err := LoadDefinitions(path)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func TestWithSettingsOverridesCommandFieldsAndKeepsFileUntouched(t *testing.T) {
	file := writeSettingsFixture(t)
	merged, problems := file.WithSettings(Settings{Commands: map[string]CommandSettings{
		"plan": {Executor: "opencode", Timeout: "5m", Prompt: "New plan for {{task.spec}}", Model: "opus"},
	}})
	if len(problems) != 0 {
		t.Fatalf("problems = %+v", problems)
	}
	command, err := merged.ResolveCommand("plan")
	if err != nil {
		t.Fatal(err)
	}
	if command.Executor != "opencode" || command.Timeout.String() != "5m0s" || command.Prompt != "New plan for {{task.spec}}" {
		t.Fatalf("merged command = %+v", command)
	}
	original, err := file.ResolveCommand("plan")
	if err != nil {
		t.Fatal(err)
	}
	if original.Executor != "claude" || !strings.HasPrefix(original.Prompt, "Plan ") || original.Hash == command.Hash {
		t.Fatalf("file command changed: %+v", original)
	}
	if merged.DefaultModel("plan") != "opus" || file.DefaultModel("plan") != "" {
		t.Fatal("command model override was not isolated to the merged configuration")
	}
}

func TestWithSettingsModelPrecedenceForWorkflowSteps(t *testing.T) {
	file := writeSettingsFixture(t)
	merged, problems := file.WithSettings(Settings{
		Commands:  map[string]CommandSettings{"plan": {Model: "opus"}},
		Executors: map[string]ExecutorSettings{"codex": {DefaultModel: "sol"}, "claude": {DefaultModel: "sonnet"}},
	})
	if len(problems) != 0 {
		t.Fatalf("problems = %+v", problems)
	}
	steps, err := merged.ResolveTaskWorkflow("deliver", "")
	if err != nil {
		t.Fatal(err)
	}
	if steps[0].Command.Model != "opus" || steps[1].Command.Model != "sol" {
		t.Fatalf("defaults = %q, %q", steps[0].Command.Model, steps[1].Command.Model)
	}
	explicit, err := merged.ResolveTaskWorkflow("deliver", "luna")
	if err != nil {
		t.Fatal(err)
	}
	if explicit[0].Command.Model != "luna" || explicit[1].Command.Model != "luna" {
		t.Fatal("a submitted model must win over stored defaults")
	}
}

func TestWithSettingsCreatesAndReplacesWorkflows(t *testing.T) {
	file := writeSettingsFixture(t)
	merged, problems := file.WithSettings(Settings{
		Commands: map[string]CommandSettings{"check": {Executor: "claude", Prompt: "Check {{task.output_dir}}"}},
		Workflows: map[string]WorkflowSettings{
			"deliver": {Steps: []WorkflowStepSettings{{Command: "build"}, {Command: "check", Approval: true}}},
			"review":  {Steps: []WorkflowStepSettings{{Command: "check", RequiredOutputs: []string{"report.md"}}}},
		},
	})
	if len(problems) != 0 {
		t.Fatalf("problems = %+v", problems)
	}
	steps, err := merged.ResolveTaskWorkflow("deliver", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[0].Command.Name != "build" || steps[0].Approval || !steps[1].Approval {
		t.Fatalf("steps = %+v", steps)
	}
	editable, err := merged.WorkflowSettingsFor("review")
	if err != nil || len(editable.Steps) != 1 || editable.Steps[0].RequiredOutputs[0] != "report.md" {
		t.Fatalf("editable = %+v, %v", editable, err)
	}
}

func TestWithSettingsSkipsSettingsThatBreakResolution(t *testing.T) {
	file := writeSettingsFixture(t)
	merged, problems := file.WithSettings(Settings{
		Commands: map[string]CommandSettings{
			"plan":   {Prompt: "Uses {{task.unknown}}"},
			"orphan": {Prompt: "No executor {{task.spec}}"},
			"build":  {Timeout: "-1s"},
		},
		Workflows: map[string]WorkflowSettings{
			"empty":   {},
			"missing": {Steps: []WorkflowStepSettings{{Command: "nope"}}},
		},
		Executors: map[string]ExecutorSettings{"codex": {DefaultModel: "bad\nmodel"}},
	})
	if len(problems) != 6 {
		t.Fatalf("problems = %+v", problems)
	}
	for _, problem := range problems {
		if problem.Error == "" || problem.Kind == "" || problem.Name == "" {
			t.Fatalf("incomplete problem %+v", problem)
		}
	}
	command, err := merged.ResolveCommand("plan")
	if err != nil || !strings.HasPrefix(command.Prompt, "Plan ") {
		t.Fatalf("file prompt should remain in effect: %+v %v", command, err)
	}
	if _, ok := merged.Workflows["empty"]; ok {
		t.Fatal("invalid workflow was applied")
	}
	if merged.DefaultModel("build") != "" {
		t.Fatal("invalid executor default was applied")
	}
}

func TestWithSettingsRejectsCommandChangeThatBreaksFileWorkflow(t *testing.T) {
	file := writeSettingsFixture(t)
	// A {{machinist.prompt}}-only prompt is valid for a command, but the same
	// template must still render for the workflow that uses it.
	_, problems := file.WithSettings(Settings{Commands: map[string]CommandSettings{"plan": {Prompt: "Hi {{stage.nope}}"}}})
	if len(problems) != 1 || problems[0].Name != "plan" {
		t.Fatalf("problems = %+v", problems)
	}
}

func TestPromptWarnings(t *testing.T) {
	if warnings := PromptWarnings("short\n\n\nprompt"); len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if warnings := PromptWarnings(strings.Repeat("a", 2049)); len(warnings) != 1 || !strings.Contains(warnings[0], "bytes") {
		t.Fatalf("warnings = %v", warnings)
	}
	lines := strings.Repeat("do it\n\n", 41)
	if warnings := PromptWarnings(lines); len(warnings) != 1 || !strings.Contains(warnings[0], "41 non-empty lines") {
		t.Fatalf("warnings = %v", warnings)
	}
}
