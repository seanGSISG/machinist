package config

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// InvalidCommand describes a command or workflow entry that was excluded from
// a loaded configuration.
type InvalidCommand struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
	kind   string
}

// Report is the usable portion of a configuration and any entries that were
// excluded from it.
type Report struct {
	Config          Config           `json:"-"`
	InvalidCommands []InvalidCommand `json:"invalid_commands"`
}

type rawEntriesConfig struct {
	Storage   Storage                        `toml:"storage"`
	Workflows map[string]unstable.RawMessage `toml:"workflows"`
	Server    Server                         `toml:"server"`
	Commands  map[string]unstable.RawMessage `toml:"commands"`
	GitHub    GitHub                         `toml:"github"`
	Triggers  TriggerDefinitions             `toml:"triggers"`
}

// ValidateFile parses a Machinist configuration, then decodes and validates
// each command and workflow independently. Invalid entries are omitted from
// Report.Config and returned as diagnostics. File-level errors are returned.
func ValidateFile(path string) (Report, error) {
	if path == "" {
		defaultPath, err := defaultConfigPath("config.toml")
		if err != nil {
			return Report{}, err
		}
		path = defaultPath
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return Report{}, fmt.Errorf("resolve Machinist config: %w", err)
	}
	body, err := readBoundedFile(absPath, maxConfigBytes)
	if err != nil {
		return Report{}, fmt.Errorf("read Machinist config %q: %w", absPath, err)
	}

	// Phase one parses the complete document. No partial configuration is safe
	// to use when its TOML syntax is invalid.
	var raw map[string]any
	if err := toml.Unmarshal(body, &raw); err != nil {
		return Report{}, fmt.Errorf("parse Machinist config %q: %w", absPath, err)
	}
	if _, ok := raw["pipelines"]; ok {
		return Report{}, fmt.Errorf("parse Machinist config %q: pipelines were removed; replace each pipeline with a repository-owned orchestration script configured under [commands]", absPath)
	}
	if _, ok := raw["shepherd"]; ok {
		return Report{}, fmt.Errorf("parse Machinist config %q: shepherd schedules were removed; schedule a command with a [triggers.cron.NAME] or [triggers.interval.NAME] trigger", absPath)
	}
	if _, ok := raw["agents"]; ok {
		return Report{}, fmt.Errorf("parse Machinist config %q: agents were renamed to commands; move [agents.NAME] definitions to [commands.NAME] and use --command", absPath)
	}
	if err := validateTriggerKeys(raw); err != nil {
		return Report{}, fmt.Errorf("parse Machinist config %q: %w", absPath, err)
	}

	// Phase two captures entry bodies without decoding them, while retaining
	// strict decoding for every non-entry section.
	var sections rawEntriesConfig
	decoder := toml.NewDecoder(bytes.NewReader(body))
	decoder.EnableUnmarshalerInterface()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&sections); err != nil {
		return Report{}, fmt.Errorf("parse Machinist config %q: %w", absPath, err)
	}

	loaded := Config{
		Storage: sections.Storage, Server: sections.Server, GitHub: sections.GitHub,
		Triggers: sections.Triggers, Commands: map[string]Command{},
		Workflows: map[string]Workflow{}, path: absPath,
	}
	report := Report{Config: loaded, InvalidCommands: []InvalidCommand{}}
	entryOffsets := configEntryOffsets(body)
	for _, name := range sortedMapKeys(sections.Commands) {
		rawEntry := sections.Commands[name]
		var command Command
		entryDecoder := toml.NewDecoder(bytes.NewReader(rawEntry))
		entryDecoder.DisallowUnknownFields()
		if err := entryDecoder.Decode(&command); err != nil {
			report.InvalidCommands = append(report.InvalidCommands, invalidEntry(absPath, body, rawEntry, "command", name, entryOffsets[entryKey("commands", name)], err))
			continue
		}
		report.Config.Commands[name] = command
		if _, err := report.Config.ResolveCommand(name); err != nil {
			delete(report.Config.Commands, name)
			report.InvalidCommands = append(report.InvalidCommands, invalidEntry(absPath, body, rawEntry, "command", name, entryOffsets[entryKey("commands", name)], err))
		}
	}
	for _, name := range sortedMapKeys(sections.Workflows) {
		rawEntry := sections.Workflows[name]
		var workflow Workflow
		entryDecoder := toml.NewDecoder(bytes.NewReader(rawEntry))
		entryDecoder.DisallowUnknownFields()
		if err := entryDecoder.Decode(&workflow); err != nil {
			report.InvalidCommands = append(report.InvalidCommands, invalidEntry(absPath, body, rawEntry, "workflow", name, entryOffsets[entryKey("workflows", name)], err))
			continue
		}
		report.Config.Workflows[name] = workflow
		if _, err := report.Config.ResolveTaskWorkflow(name, ""); err != nil {
			delete(report.Config.Workflows, name)
			report.InvalidCommands = append(report.InvalidCommands, invalidEntry(absPath, body, rawEntry, "workflow", name, entryOffsets[entryKey("workflows", name)], err))
		}
	}
	return report, nil
}

func invalidEntry(path string, body []byte, rawEntry unstable.RawMessage, kind, name string, tableOffset int, err error) InvalidCommand {
	line, column := entryStart(body, rawEntry, tableOffset)
	var decodeErr *toml.DecodeError
	if errors.As(err, &decodeErr) {
		localLine, localColumn := decodeErr.Position()
		line += localLine - 1
		column = localColumn
	}
	message := strings.TrimPrefix(err.Error(), "toml: ")
	return InvalidCommand{Name: name, Reason: fmt.Sprintf("%s:%d:%d: %s", path, line, column, message), kind: kind}
}

func entryStart(body []byte, rawEntry unstable.RawMessage, tableOffset int) (int, int) {
	entry := bytes.TrimLeft(rawEntry, " \t\r\n")
	if tableOffset < 0 || tableOffset >= len(body) {
		tableOffset = 0
	}
	relativeOffset := bytes.Index(body[tableOffset:], entry)
	if relativeOffset < 0 {
		return 1, 1
	}
	offset := tableOffset + relativeOffset
	lineStart := bytes.LastIndexByte(body[:offset], '\n') + 1
	return bytes.Count(body[:offset], []byte{'\n'}) + 1, offset - lineStart + 1
}

func configEntryOffsets(body []byte) map[string]int {
	offsets := map[string]int{}
	parser := unstable.Parser{}
	parser.Reset(body)
	for parser.NextExpression() {
		expression := parser.Expression()
		if expression.Kind != unstable.Table {
			continue
		}
		parts := []string{}
		tableOffset := 0
		keys := expression.Key()
		for keys.Next() {
			if len(parts) == 0 {
				tableOffset = int(keys.Node().Raw.Offset)
			}
			parts = append(parts, string(keys.Node().Data))
		}
		if len(parts) == 2 && (parts[0] == "commands" || parts[0] == "workflows") {
			offsets[entryKey(parts[0], parts[1])] = tableOffset
		}
	}
	return offsets
}

func entryKey(section, name string) string { return section + "\x00" + name }
