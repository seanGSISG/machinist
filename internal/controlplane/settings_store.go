package controlplane

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

// settingsSchema is additive: older releases ignore these tables, so the
// schema version does not change and a rollback needs no migration.
const settingsSchema = `
CREATE TABLE IF NOT EXISTS settings_versions (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 kind TEXT NOT NULL CHECK(kind IN ('commands','workflows','executors')),
 name TEXT NOT NULL, body TEXT NOT NULL, reverted_from INTEGER, created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS settings_versions_current ON settings_versions(kind,name,id);
CREATE TABLE IF NOT EXISTS worker_executors (
 worker_instance TEXT NOT NULL REFERENCES workers(instance_id) ON DELETE CASCADE,
 executor TEXT NOT NULL, models TEXT NOT NULL DEFAULT '[]', supports_model INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(worker_instance,executor));
`

var (
	ErrSettingConflict = errors.New("setting changed since it was loaded")
	ErrSettingInvalid  = errors.New("invalid setting")
)

// SettingVersion is one immutable row of settings history. A null Value
// means "no override": the config.toml definition applies.
type SettingVersion struct {
	ID           int64           `json:"id"`
	Kind         string          `json:"kind"`
	Name         string          `json:"name"`
	Value        json.RawMessage `json:"value"`
	RevertedFrom *int64          `json:"reverted_from,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

// ExecutorCapability is what registered workers advertise for one executor.
type ExecutorCapability struct {
	Name          string   `json:"name"`
	Workers       []string `json:"workers"`
	Models        []string `json:"models"`
	SupportsModel bool     `json:"supports_model"`
	// AnyModel is true when a worker supports model selection without aliases.
	AnyModel bool `json:"any_model"`
}

// AllowsModel reports whether at least one worker can run this executor with model.
func (c ExecutorCapability) AllowsModel(model string) bool {
	return model == "" || c.SupportsModel && (c.AnyModel || slices.Contains(c.Models, model))
}

// SettingsState is the current settings layer and its version per item.
type SettingsState struct {
	Settings config.Settings
	Versions map[string]int64
}

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func settingKey(kind, name string) string { return kind + "/" + name }

// SettingChange appends one version. BaseVersion must match the current
// version unless the change is a revert.
type SettingChange struct {
	Kind         string
	Name         string
	Value        json.RawMessage
	BaseVersion  int64
	RevertedFrom *int64
}

// SettingsValidator checks the settings that would result from a change.
type SettingsValidator func(before, after config.Settings, executors map[string]ExecutorCapability) error

func recordWorkerExecutors(ctx context.Context, tx *sql.Tx, request protocol.PollRequest) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM worker_executors WHERE worker_instance=?`, request.InstanceID); err != nil {
		return fmt.Errorf("clear worker executors: %w", err)
	}
	for executor := range stringSet(request.Executors) {
		models, supportsModel := request.Models[executor]
		if models == nil {
			models = []string{}
		}
		encoded, err := json.Marshal(models)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO worker_executors(worker_instance,executor,models,supports_model) VALUES(?,?,?,?)`, request.InstanceID, executor, string(encoded), supportsModel); err != nil {
			return fmt.Errorf("store worker executor: %w", err)
		}
	}
	return nil
}

// ExecutorCapabilities lists every executor advertised by a registered worker.
func (s *Store) ExecutorCapabilities(ctx context.Context) (map[string]ExecutorCapability, error) {
	return executorCapabilities(ctx, s.db)
}

func executorCapabilities(ctx context.Context, q querier) (map[string]ExecutorCapability, error) {
	rows, err := q.QueryContext(ctx, `SELECT e.executor,w.name,e.models,e.supports_model FROM worker_executors e JOIN workers w ON w.instance_id=e.worker_instance ORDER BY e.executor,w.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	capabilities := map[string]ExecutorCapability{}
	for rows.Next() {
		var executor, worker, encoded string
		var supportsModel bool
		if err := rows.Scan(&executor, &worker, &encoded, &supportsModel); err != nil {
			return nil, err
		}
		var models []string
		if err := json.Unmarshal([]byte(encoded), &models); err != nil {
			return nil, err
		}
		capability := capabilities[executor]
		capability.Name = executor
		if !slices.Contains(capability.Workers, worker) {
			capability.Workers = append(capability.Workers, worker)
		}
		if supportsModel {
			capability.SupportsModel = true
			capability.AnyModel = capability.AnyModel || len(models) == 0
			for _, model := range models {
				if !slices.Contains(capability.Models, model) {
					capability.Models = append(capability.Models, model)
				}
			}
		}
		capabilities[executor] = capability
	}
	for name, capability := range capabilities {
		if capability.Models == nil {
			capability.Models = []string{}
		}
		slices.Sort(capability.Models)
		capabilities[name] = capability
	}
	return capabilities, rows.Err()
}

// CurrentSettings returns the newest version of every setting.
func (s *Store) CurrentSettings(ctx context.Context) (SettingsState, error) {
	return currentSettings(ctx, s.db)
}

func currentSettings(ctx context.Context, q querier) (SettingsState, error) {
	state := SettingsState{
		Settings: config.Settings{Commands: map[string]config.CommandSettings{}, Workflows: map[string]config.WorkflowSettings{}, Executors: map[string]config.ExecutorSettings{}},
		Versions: map[string]int64{},
	}
	rows, err := q.QueryContext(ctx, `SELECT s.id,s.kind,s.name,s.body FROM settings_versions s WHERE s.id=(SELECT MAX(id) FROM settings_versions l WHERE l.kind=s.kind AND l.name=s.name)`)
	if err != nil {
		return SettingsState{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var kind, name, body string
		if err := rows.Scan(&id, &kind, &name, &body); err != nil {
			return SettingsState{}, err
		}
		state.Versions[settingKey(kind, name)] = id
		if err := setSetting(&state.Settings, kind, name, json.RawMessage(body)); err != nil {
			return SettingsState{}, fmt.Errorf("setting %s version %d: %w", settingKey(kind, name), id, err)
		}
	}
	return state, rows.Err()
}

// setSetting replaces or, for a null value, removes one setting.
func setSetting(settings *config.Settings, kind, name string, value json.RawMessage) error {
	null := string(value) == "null"
	switch kind {
	case config.SettingsCommands:
		delete(settings.Commands, name)
		if !null {
			var command config.CommandSettings
			if err := strictUnmarshal(value, &command); err != nil {
				return err
			}
			settings.Commands[name] = command
		}
	case config.SettingsWorkflows:
		delete(settings.Workflows, name)
		if !null {
			var workflow config.WorkflowSettings
			if err := strictUnmarshal(value, &workflow); err != nil {
				return err
			}
			settings.Workflows[name] = workflow
		}
	case config.SettingsExecutors:
		delete(settings.Executors, name)
		if !null {
			var executor config.ExecutorSettings
			if err := strictUnmarshal(value, &executor); err != nil {
				return err
			}
			settings.Executors[name] = executor
		}
	default:
		return fmt.Errorf("unknown settings kind %q", kind)
	}
	return nil
}

// SaveSetting validates and appends one setting version atomically.
func (s *Store) SaveSetting(ctx context.Context, change SettingChange, validate SettingsValidator) (SettingVersion, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SettingVersion{}, err
	}
	defer tx.Rollback()
	state, err := currentSettings(ctx, tx)
	if err != nil {
		return SettingVersion{}, err
	}
	if change.RevertedFrom == nil && state.Versions[settingKey(change.Kind, change.Name)] != change.BaseVersion {
		return SettingVersion{}, ErrSettingConflict
	}
	before := config.Settings{Commands: maps.Clone(state.Settings.Commands), Workflows: maps.Clone(state.Settings.Workflows), Executors: maps.Clone(state.Settings.Executors)}
	if err := setSetting(&state.Settings, change.Kind, change.Name, change.Value); err != nil {
		return SettingVersion{}, fmt.Errorf("%w: %v", ErrSettingInvalid, err)
	}
	executors, err := executorCapabilities(ctx, tx)
	if err != nil {
		return SettingVersion{}, err
	}
	if err := validate(before, state.Settings, executors); err != nil {
		return SettingVersion{}, fmt.Errorf("%w: %v", ErrSettingInvalid, err)
	}
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `INSERT INTO settings_versions(kind,name,body,reverted_from,created_at) VALUES(?,?,?,?,?)`, change.Kind, change.Name, string(change.Value), change.RevertedFrom, now.Format(time.RFC3339Nano))
	if err != nil {
		return SettingVersion{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return SettingVersion{}, err
	}
	if err := tx.Commit(); err != nil {
		return SettingVersion{}, err
	}
	return SettingVersion{ID: id, Kind: change.Kind, Name: change.Name, Value: change.Value, RevertedFrom: change.RevertedFrom, CreatedAt: now}, nil
}

// SettingHistory lists the versions of one setting, newest first.
func (s *Store) SettingHistory(ctx context.Context, kind, name string) ([]SettingVersion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,name,body,reverted_from,created_at FROM settings_versions WHERE kind=? AND name=? ORDER BY id DESC LIMIT 200`, kind, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := []SettingVersion{}
	for rows.Next() {
		version, err := scanSettingVersion(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

// SettingVersionByID loads one historical version.
func (s *Store) SettingVersionByID(ctx context.Context, id int64) (SettingVersion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,name,body,reverted_from,created_at FROM settings_versions WHERE id=?`, id)
	if err != nil {
		return SettingVersion{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return SettingVersion{}, err
		}
		return SettingVersion{}, sql.ErrNoRows
	}
	return scanSettingVersion(rows)
}

func scanSettingVersion(rows *sql.Rows) (SettingVersion, error) {
	var version SettingVersion
	var body, createdAt string
	var revertedFrom sql.NullInt64
	if err := rows.Scan(&version.ID, &version.Kind, &version.Name, &body, &revertedFrom, &createdAt); err != nil {
		return SettingVersion{}, err
	}
	version.Value = json.RawMessage(body)
	if revertedFrom.Valid {
		version.RevertedFrom = &revertedFrom.Int64
	}
	version.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return version, nil
}

func strictUnmarshal(value json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
