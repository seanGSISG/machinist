package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Setting kinds as they appear in the settings API and database.
const (
	SettingsCommands  = "commands"
	SettingsWorkflows = "workflows"
	SettingsExecutors = "executors"
)

const (
	maxModelBytes     = 128
	promptWarnBytes   = 2048
	promptWarnLines   = 40
	maxSettingNameLen = 64
)

// CommandSettings overrides fields of one command. Empty fields inherit from
// config.toml. A command absent from the file must set Executor.
type CommandSettings struct {
	Executor string `json:"executor,omitempty"`
	Model    string `json:"model,omitempty"`
	Timeout  string `json:"timeout,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
}

// WorkflowStepSettings is one ordered workflow step.
type WorkflowStepSettings struct {
	Command         string   `json:"command"`
	Approval        bool     `json:"approval,omitempty"`
	ID              string   `json:"id,omitempty"`
	RequiredOutputs []string `json:"required_outputs,omitempty"`
}

// WorkflowSettings replaces a workflow of the same name from config.toml.
type WorkflowSettings struct {
	Steps []WorkflowStepSettings `json:"steps"`
}

// ExecutorSettings holds control-plane defaults for a worker-declared executor.
// The executor command itself is never configurable here.
type ExecutorSettings struct {
	DefaultModel string `json:"default_model,omitempty"`
}

// Settings is the current database layer applied over config.toml.
type Settings struct {
	Commands  map[string]CommandSettings
	Workflows map[string]WorkflowSettings
	Executors map[string]ExecutorSettings
}

// SettingsProblem reports a stored setting that could not be applied.
type SettingsProblem struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Error string `json:"error"`
}

// ValidSettingName reports whether name can key a command, workflow, or executor setting.
func ValidSettingName(name string) bool {
	return len(name) > 0 && len(name) <= maxSettingNameLen && identifier.MatchString(name)
}

// ValidModel reports whether a model alias is a single short line.
func ValidModel(model string) bool {
	return len(model) <= maxModelBytes && !strings.ContainsAny(model, "\x00\r\n")
}

// PromptWarnings returns advisory messages for prompts that are too large to
// stay small and code-driven. They never block a change.
func PromptWarnings(prompt string) []string {
	warnings := []string{}
	if len(prompt) > promptWarnBytes {
		warnings = append(warnings, fmt.Sprintf("prompt is %d bytes; keep prompts under about %d bytes", len(prompt), promptWarnBytes))
	}
	lines := 0
	for line := range strings.SplitSeq(prompt, "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	if lines > promptWarnLines {
		warnings = append(warnings, fmt.Sprintf("prompt has %d non-empty lines; keep prompts under about %d instructions", lines, promptWarnLines))
	}
	return warnings
}

// DefaultModel returns the model a new task uses for a command when the
// submitter does not choose one: the command setting, then the executor default.
func (c Config) DefaultModel(commandName string) string {
	command, ok := c.Commands[commandName]
	if !ok {
		return ""
	}
	if command.model != "" {
		return command.model
	}
	return c.executorModels[command.Executor]
}

// WithSettings applies stored settings over the file configuration. Each
// setting is applied only when it does not make a command or workflow fail to
// resolve; rejected settings are returned as problems and the file value stays
// in effect. Commands apply first, then executors, then workflows.
func (c Config) WithSettings(settings Settings) (Config, []SettingsProblem) {
	merged := c.clone()
	baseline := merged.resolutionErrors()
	problems := []SettingsProblem{}
	apply := func(kind, name string, change func(*Config) error) {
		candidate := merged.clone()
		if err := change(&candidate); err != nil {
			problems = append(problems, SettingsProblem{Kind: kind, Name: name, Error: err.Error()})
			return
		}
		after := candidate.resolutionErrors()
		for _, key := range slices.Sorted(maps.Keys(after)) {
			if _, existed := baseline[key]; !existed {
				problems = append(problems, SettingsProblem{Kind: kind, Name: name, Error: after[key]})
				return
			}
		}
		merged, baseline = candidate, after
	}
	for _, name := range sortedMapKeys(settings.Commands) {
		value := settings.Commands[name]
		apply(SettingsCommands, name, func(target *Config) error { return target.applyCommand(name, value) })
	}
	for _, name := range sortedMapKeys(settings.Executors) {
		value := settings.Executors[name]
		apply(SettingsExecutors, name, func(target *Config) error {
			if !ValidSettingName(name) {
				return errors.New("invalid executor name")
			}
			if !ValidModel(strings.TrimSpace(value.DefaultModel)) {
				return fmt.Errorf("executor %q default model must be at most %d bytes on one line", name, maxModelBytes)
			}
			if model := strings.TrimSpace(value.DefaultModel); model != "" {
				target.executorModels[name] = model
			}
			return nil
		})
	}
	for _, name := range sortedMapKeys(settings.Workflows) {
		value := settings.Workflows[name]
		apply(SettingsWorkflows, name, func(target *Config) error { return target.applyWorkflow(name, value) })
	}
	return merged, problems
}

func (c *Config) applyCommand(name string, value CommandSettings) error {
	if !ValidSettingName(name) {
		return errors.New("invalid command name")
	}
	command, exists := c.Commands[name]
	if executor := strings.TrimSpace(value.Executor); executor != "" {
		if !ValidSettingName(executor) {
			return errors.New("invalid executor name")
		}
		command.Executor = executor
	} else if !exists {
		return fmt.Errorf("command %q is not in config.toml, so it needs an executor", name)
	}
	if timeout := strings.TrimSpace(value.Timeout); timeout != "" {
		command.Timeout = timeout
	}
	if value.Prompt != "" {
		command.prompt = value.Prompt
	}
	model := strings.TrimSpace(value.Model)
	if !ValidModel(model) {
		return fmt.Errorf("command %q model must be at most %d bytes on one line", name, maxModelBytes)
	}
	command.model = model
	c.Commands[name] = command
	_, err := c.ResolveCommand(name)
	return err
}

func (c *Config) applyWorkflow(name string, value WorkflowSettings) error {
	if !ValidSettingName(name) {
		return errors.New("invalid workflow name")
	}
	steps := make([]any, 0, len(value.Steps))
	for _, step := range value.Steps {
		if step.Approval || step.ID != "" || len(step.RequiredOutputs) > 0 {
			table := map[string]any{"command": step.Command}
			if step.Approval {
				table["approval"] = "before"
			}
			if step.ID != "" {
				table["id"] = step.ID
			}
			if len(step.RequiredOutputs) > 0 {
				outputs := make([]any, 0, len(step.RequiredOutputs))
				for _, output := range step.RequiredOutputs {
					outputs = append(outputs, output)
				}
				table["required_outputs"] = outputs
			}
			steps = append(steps, table)
			continue
		}
		steps = append(steps, step.Command)
	}
	c.Workflows[name] = Workflow{Steps: steps}
	_, err := c.ResolveTaskWorkflow(name, "")
	return err
}

// WorkflowSettingsFor converts a resolved workflow into its editable form.
func (c Config) WorkflowSettingsFor(name string) (WorkflowSettings, error) {
	steps, err := c.ResolveTaskWorkflow(name, "")
	if err != nil {
		return WorkflowSettings{}, err
	}
	result := WorkflowSettings{Steps: make([]WorkflowStepSettings, 0, len(steps))}
	for _, step := range steps {
		editable := WorkflowStepSettings{Command: step.Command.Name, Approval: step.Approval, RequiredOutputs: step.RequiredOutputs}
		if step.ID != step.Command.Name {
			editable.ID = step.ID
		}
		result.Steps = append(result.Steps, editable)
	}
	return result, nil
}

// resolutionErrors maps "kind/name" to the error for every definition that
// currently fails to resolve.
func (c Config) resolutionErrors() map[string]string {
	failures := map[string]string{}
	for name := range c.Commands {
		if _, err := c.ResolveCommand(name); err != nil {
			failures[SettingsCommands+"/"+name] = err.Error()
		}
	}
	for name := range c.Workflows {
		if _, err := c.ResolveTaskWorkflow(name, ""); err != nil {
			failures[SettingsWorkflows+"/"+name] = err.Error()
		}
	}
	return failures
}

func (c Config) clone() Config {
	c.Commands = maps.Clone(c.Commands)
	if c.Commands == nil {
		c.Commands = map[string]Command{}
	}
	c.Workflows = maps.Clone(c.Workflows)
	if c.Workflows == nil {
		c.Workflows = map[string]Workflow{}
	}
	c.executorModels = maps.Clone(c.executorModels)
	if c.executorModels == nil {
		c.executorModels = map[string]string{}
	}
	return c
}
