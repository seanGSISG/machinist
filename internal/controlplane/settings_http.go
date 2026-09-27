package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/owainlewis/machinist/internal/config"
)

type commandValues struct {
	Executor   string `json:"executor"`
	Model      string `json:"model,omitempty"`
	Timeout    string `json:"timeout"`
	Prompt     string `json:"prompt"`
	PromptFile string `json:"prompt_file,omitempty"`
}

type commandSettingsView struct {
	Name      string                  `json:"name"`
	Version   int64                   `json:"version"`
	File      *commandValues          `json:"file"`
	Override  *config.CommandSettings `json:"override"`
	Effective *commandValues          `json:"effective"`
	Error     string                  `json:"error,omitempty"`
	Warnings  []string                `json:"warnings"`
}

type workflowSettingsView struct {
	Name      string                   `json:"name"`
	Version   int64                    `json:"version"`
	File      *config.WorkflowSettings `json:"file"`
	Override  *config.WorkflowSettings `json:"override"`
	Effective *config.WorkflowSettings `json:"effective"`
	Error     string                   `json:"error,omitempty"`
}

type executorSettingsView struct {
	ExecutorCapability
	Version      int64                    `json:"version"`
	Override     *config.ExecutorSettings `json:"override"`
	DefaultModel string                   `json:"default_model"`
}

type settingsResponse struct {
	Commands  []commandSettingsView    `json:"commands"`
	Workflows []workflowSettingsView   `json:"workflows"`
	Executors []executorSettingsView   `json:"executors"`
	Problems  []config.SettingsProblem `json:"problems"`
}

type settingChangeRequest struct {
	BaseVersion int64           `json:"base_version"`
	Value       json.RawMessage `json:"value"`
}

type settingChangeResponse struct {
	Version  SettingVersion `json:"version"`
	Warnings []string       `json:"warnings"`
}

// loadDefinitions returns config.toml with the stored settings applied.
func (s *Server) loadDefinitions(ctx context.Context) (config.Config, []config.SettingsProblem, error) {
	file, err := config.LoadDefinitions(s.definitionPath)
	if err != nil {
		return config.Config{}, nil, err
	}
	state, err := s.store.CurrentSettings(ctx)
	if err != nil {
		return config.Config{}, nil, err
	}
	if len(state.Versions) == 0 {
		return file, []config.SettingsProblem{}, nil
	}
	merged, problems := file.WithSettings(state.Settings)
	return merged, problems, nil
}

func (s *Server) settings(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	ctx := request.Context()
	file, err := config.LoadDefinitions(s.definitionPath)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	state, err := s.store.CurrentSettings(ctx)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	capabilities, err := s.store.ExecutorCapabilities(ctx)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	merged, problems := file.WithSettings(state.Settings)
	result := settingsResponse{Commands: []commandSettingsView{}, Workflows: []workflowSettingsView{}, Executors: []executorSettingsView{}, Problems: problems}
	executorNames := map[string]bool{}
	for name := range capabilities {
		executorNames[name] = true
	}
	for name := range state.Settings.Executors {
		executorNames[name] = true
	}
	for _, name := range unionNames(file.CommandNames(), keys(state.Settings.Commands)) {
		view := commandSettingsView{Name: name, Version: state.Versions[settingKey(config.SettingsCommands, name)], Warnings: []string{}}
		if command, ok := file.Commands[name]; ok {
			view.File = &commandValues{Executor: command.Executor, Timeout: command.Timeout, PromptFile: command.PromptFile}
			if resolved, err := file.ResolveCommand(name); err == nil {
				view.File.Prompt = resolved.Prompt
				view.File.Timeout = resolved.Timeout.String()
			}
		}
		if override, ok := state.Settings.Commands[name]; ok {
			view.Override = &override
		}
		if resolved, err := merged.ResolveCommand(name); err == nil {
			view.Effective = &commandValues{Executor: resolved.Executor, Model: merged.DefaultModel(name), Timeout: resolved.Timeout.String(), Prompt: resolved.Prompt}
			view.Warnings = config.PromptWarnings(resolved.Prompt)
			executorNames[resolved.Executor] = true
		} else if view.File != nil || view.Override != nil {
			view.Error = err.Error()
		}
		result.Commands = append(result.Commands, view)
	}
	for _, name := range unionNames(file.WorkflowNames(), keys(state.Settings.Workflows)) {
		view := workflowSettingsView{Name: name, Version: state.Versions[settingKey(config.SettingsWorkflows, name)]}
		if _, ok := file.Workflows[name]; ok {
			if steps, err := file.WorkflowSettingsFor(name); err == nil {
				view.File = &steps
			}
		}
		if override, ok := state.Settings.Workflows[name]; ok {
			view.Override = &override
		}
		if steps, err := merged.WorkflowSettingsFor(name); err == nil {
			view.Effective = &steps
		} else {
			view.Error = err.Error()
		}
		result.Workflows = append(result.Workflows, view)
	}
	for _, name := range keys(executorNames) {
		capability := capabilities[name]
		capability.Name = name
		if capability.Workers == nil {
			capability.Workers = []string{}
		}
		if capability.Models == nil {
			capability.Models = []string{}
		}
		view := executorSettingsView{ExecutorCapability: capability, Version: state.Versions[settingKey(config.SettingsExecutors, name)], DefaultModel: merged.ExecutorDefaultModel(name)}
		if override, ok := state.Settings.Executors[name]; ok {
			view.Override = &override
		}
		result.Executors = append(result.Executors, view)
	}
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) settingHistory(response http.ResponseWriter, request *http.Request) {
	kind, name, ok := settingTarget(response, request)
	if !ok {
		return
	}
	versions, err := s.store.SettingHistory(request.Context(), kind, name)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, map[string]any{"versions": versions})
}

func (s *Server) putSetting(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	kind, name, ok := settingTarget(response, request)
	if !ok {
		return
	}
	var input settingChangeRequest
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if len(input.Value) == 0 {
		writeError(response, http.StatusBadRequest, errors.New("value is required; send null to remove the override"))
		return
	}
	value, err := normalizeSettingValue(kind, input.Value)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	s.saveSetting(response, request, SettingChange{Kind: kind, Name: name, Value: value, BaseVersion: input.BaseVersion})
}

func (s *Server) revertSetting(response http.ResponseWriter, request *http.Request) {
	id, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(response, http.StatusBadRequest, errors.New("invalid version id"))
		return
	}
	version, err := s.store.SettingVersionByID(request.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusNotFound, errors.New("setting version not found"))
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	s.saveSetting(response, request, SettingChange{Kind: version.Kind, Name: version.Name, Value: version.Value, RevertedFrom: &id})
}

func (s *Server) saveSetting(response http.ResponseWriter, request *http.Request, change SettingChange) {
	file, err := config.LoadDefinitions(s.definitionPath)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	version, err := s.store.SaveSetting(request.Context(), change, settingsValidator(file, change.Kind, change.Name))
	if errors.Is(err, ErrSettingConflict) {
		writeError(response, http.StatusConflict, errors.New("this setting changed since you loaded it; reload and try again"))
		return
	}
	if errors.Is(err, ErrSettingInvalid) {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	warnings := []string{}
	if change.Kind == config.SettingsCommands {
		var command config.CommandSettings
		if json.Unmarshal(change.Value, &command) == nil && command.Prompt != "" {
			warnings = config.PromptWarnings(command.Prompt)
		}
	}
	writeJSON(response, http.StatusOK, settingChangeResponse{Version: version, Warnings: warnings})
}

// settingsValidator enforces worker authority for the changed setting and
// rejects changes that stop any command or workflow from resolving.
func settingsValidator(file config.Config, kind, name string) SettingsValidator {
	return func(before, after config.Settings, executors map[string]ExecutorCapability) error {
		if err := checkWorkerAuthority(file, after, kind, name, executors); err != nil {
			return err
		}
		_, beforeProblems := file.WithSettings(before)
		_, afterProblems := file.WithSettings(after)
		known := map[string]bool{}
		for _, problem := range beforeProblems {
			known[settingKey(problem.Kind, problem.Name)] = true
		}
		for _, problem := range afterProblems {
			if problem.Kind == kind && problem.Name == name {
				return errors.New(problem.Error)
			}
		}
		for _, problem := range afterProblems {
			if !known[settingKey(problem.Kind, problem.Name)] {
				return fmt.Errorf("this change would break %s %q: %s", strings.TrimSuffix(problem.Kind, "s"), problem.Name, problem.Error)
			}
		}
		return nil
	}
}

func checkWorkerAuthority(file config.Config, after config.Settings, kind, name string, executors map[string]ExecutorCapability) error {
	switch kind {
	case config.SettingsCommands:
		command, ok := after.Commands[name]
		if !ok {
			return nil
		}
		executor := command.Executor
		if executor != "" {
			if _, advertised := executors[executor]; !advertised {
				return fmt.Errorf("executor %q is not advertised by any registered worker", executor)
			}
		} else {
			executor = file.Commands[name].Executor
		}
		if command.Model != "" && !executors[executor].AllowsModel(command.Model) {
			return fmt.Errorf("no registered worker offers model %q for executor %q", command.Model, executor)
		}
	case config.SettingsExecutors:
		setting, ok := after.Executors[name]
		if !ok {
			return nil
		}
		capability, advertised := executors[name]
		if !advertised {
			return fmt.Errorf("executor %q is not advertised by any registered worker", name)
		}
		if !capability.AllowsModel(setting.DefaultModel) {
			return fmt.Errorf("no registered worker offers model %q for executor %q", setting.DefaultModel, name)
		}
	}
	return nil
}

// normalizeSettingValue strictly decodes a value, trims single-line fields,
// stores an empty override as null, and returns canonical JSON.
func normalizeSettingValue(kind string, raw json.RawMessage) (json.RawMessage, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return json.RawMessage("null"), nil
	}
	var value any
	switch kind {
	case config.SettingsCommands:
		var command config.CommandSettings
		if err := strictUnmarshal(raw, &command); err != nil {
			return nil, fmt.Errorf("decode command setting: %w", err)
		}
		command.Executor, command.Model, command.Timeout = strings.TrimSpace(command.Executor), strings.TrimSpace(command.Model), strings.TrimSpace(command.Timeout)
		if command == (config.CommandSettings{}) {
			return json.RawMessage("null"), nil
		}
		value = command
	case config.SettingsWorkflows:
		var workflow config.WorkflowSettings
		if err := strictUnmarshal(raw, &workflow); err != nil {
			return nil, fmt.Errorf("decode workflow setting: %w", err)
		}
		for index := range workflow.Steps {
			workflow.Steps[index].Command = strings.TrimSpace(workflow.Steps[index].Command)
			workflow.Steps[index].ID = strings.TrimSpace(workflow.Steps[index].ID)
		}
		if workflow.Steps == nil {
			workflow.Steps = []config.WorkflowStepSettings{}
		}
		value = workflow
	case config.SettingsExecutors:
		var executor config.ExecutorSettings
		if err := strictUnmarshal(raw, &executor); err != nil {
			return nil, fmt.Errorf("decode executor setting: %w", err)
		}
		executor.DefaultModel = strings.TrimSpace(executor.DefaultModel)
		if executor.DefaultModel == "" {
			return json.RawMessage("null"), nil
		}
		value = executor
	}
	encoded, err := json.Marshal(value)
	return json.RawMessage(encoded), err
}

func settingTarget(response http.ResponseWriter, request *http.Request) (string, string, bool) {
	kind, name := request.PathValue("kind"), request.PathValue("name")
	if !slices.Contains([]string{config.SettingsCommands, config.SettingsWorkflows, config.SettingsExecutors}, kind) {
		writeError(response, http.StatusNotFound, fmt.Errorf("unknown settings kind %q", kind))
		return "", "", false
	}
	if !config.ValidSettingName(name) {
		writeError(response, http.StatusBadRequest, errors.New("names must be 1-64 letters, digits, underscores, or hyphens"))
		return "", "", false
	}
	return kind, name, true
}

func keys[T any](values map[string]T) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func unionNames(first, second []string) []string {
	names := slices.Concat(first, second)
	slices.Sort(names)
	return slices.Compact(names)
}
