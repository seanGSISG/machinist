package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	defaultLoginTimeout   = 10 * time.Minute
	minLoginTimeout       = 30 * time.Second
	maxLoginTimeout       = time.Hour
	defaultStatusInterval = 5 * time.Minute
	minStatusInterval     = 30 * time.Second
	defaultExpiringWithin = 72 * time.Hour
	defaultURLPattern     = `https://[^\s"'<>]+`
	maxStartInputs        = 8
	maxStartInputBytes    = 256
)

// ExecutorAuth is a worker-owned login recipe for one executor. The worker
// runs Login in a pseudo-terminal when an operator clicks Connect in the web
// UI, and runs Status periodically to report whether the CLI is logged in.
// The control plane only names the executor; it never supplies arguments.
type ExecutorAuth struct {
	// Login is the argument array that starts an interactive login.
	Login []string `toml:"login"`
	// Status is the argument array that exits 0 while the CLI is logged in.
	Status []string `toml:"status"`
	// ConnectedPattern, when set, must also match the status output.
	ConnectedPattern string `toml:"connected_pattern"`
	// ExpiresPattern captures an RFC 3339 time or Unix seconds from the status output.
	ExpiresPattern string `toml:"expires_pattern"`
	ExpiringWithin string `toml:"expiring_within"`
	// URLPattern, CodePattern and PromptPattern are matched against the login output.
	URLPattern    string `toml:"url_pattern"`
	CodePattern   string `toml:"code_pattern"`
	PromptPattern string `toml:"prompt_pattern"`
	// StartInput is written to the terminal once the login starts, for TUIs
	// whose login is a slash command (for example "/login\r").
	StartInput     []string `toml:"start_input"`
	Path           string   `toml:"path"`
	Timeout        string   `toml:"timeout"`
	StatusInterval string   `toml:"status_interval"`
}

// AuthRecipe is a validated ExecutorAuth.
type AuthRecipe struct {
	Executor       string
	Login          []string
	Status         []string
	Connected      *regexp.Regexp
	Expires        *regexp.Regexp
	ExpiringWithin time.Duration
	URL            *regexp.Regexp
	Code           *regexp.Regexp
	Prompt         *regexp.Regexp
	StartInput     []string
	Path           string
	Timeout        time.Duration
	StatusInterval time.Duration
}

// AuthRecipes returns the validated login recipes keyed by executor name.
func (w Worker) AuthRecipes() map[string]AuthRecipe {
	recipes := map[string]AuthRecipe{}
	for _, name := range w.ExecutorNames() {
		auth := w.Executors[name].Auth
		if auth == nil {
			continue
		}
		// Validated when the worker config loaded.
		recipe, err := auth.resolve(name)
		if err == nil {
			recipes[name] = recipe
		}
	}
	return recipes
}

func (auth ExecutorAuth) resolve(executor string) (AuthRecipe, error) {
	recipe := AuthRecipe{Executor: executor, Login: auth.Login, Status: auth.Status, StartInput: auth.StartInput, Path: strings.TrimSpace(auth.Path)}
	if len(auth.Login) == 0 && len(auth.Status) == 0 {
		return AuthRecipe{}, errors.New("must define login, status, or both")
	}
	for field, argv := range map[string][]string{"login": auth.Login, "status": auth.Status} {
		if len(argv) == 0 {
			continue
		}
		if strings.TrimSpace(argv[0]) == "" {
			return AuthRecipe{}, fmt.Errorf("%s executable must be non-empty", field)
		}
		for index, argument := range argv {
			if strings.ContainsRune(argument, '\x00') {
				return AuthRecipe{}, fmt.Errorf("%s argument %d contains a null byte", field, index)
			}
			if strings.Contains(argument, machinistParameterPrefix) {
				return AuthRecipe{}, fmt.Errorf("%s arguments cannot use Machinist parameters", field)
			}
		}
	}
	if len(auth.StartInput) > maxStartInputs {
		return AuthRecipe{}, fmt.Errorf("start_input allows at most %d entries", maxStartInputs)
	}
	for _, input := range auth.StartInput {
		if len(input) == 0 || len(input) > maxStartInputBytes {
			return AuthRecipe{}, fmt.Errorf("start_input entries must be 1-%d bytes", maxStartInputBytes)
		}
	}
	if len(auth.StartInput) > 0 && len(auth.Login) == 0 {
		return AuthRecipe{}, errors.New("start_input requires login")
	}
	if strings.ContainsAny(recipe.Path, "\x00\r\n") {
		return AuthRecipe{}, errors.New("path must be one line")
	}
	var err error
	patterns := []struct {
		field, value, fallback string
		target                 **regexp.Regexp
	}{
		{"connected_pattern", auth.ConnectedPattern, "", &recipe.Connected},
		{"expires_pattern", auth.ExpiresPattern, "", &recipe.Expires},
		{"url_pattern", auth.URLPattern, defaultURLPattern, &recipe.URL},
		{"code_pattern", auth.CodePattern, "", &recipe.Code},
		{"prompt_pattern", auth.PromptPattern, "", &recipe.Prompt},
	}
	for _, pattern := range patterns {
		value := pattern.value
		if value == "" {
			value = pattern.fallback
		}
		if value == "" {
			continue
		}
		if *pattern.target, err = regexp.Compile(value); err != nil {
			return AuthRecipe{}, fmt.Errorf("invalid %s: %w", pattern.field, err)
		}
	}
	if recipe.Expires != nil && recipe.Expires.NumSubexp() < 1 {
		return AuthRecipe{}, errors.New("expires_pattern must capture the expiry time in a group")
	}
	if len(auth.Status) == 0 && (recipe.Connected != nil || recipe.Expires != nil || auth.StatusInterval != "" || auth.ExpiringWithin != "") {
		return AuthRecipe{}, errors.New("status patterns and intervals require status")
	}
	if len(auth.Login) == 0 && (auth.URLPattern != "" || recipe.Code != nil || recipe.Prompt != nil || auth.Timeout != "") {
		return AuthRecipe{}, errors.New("login patterns and timeout require login")
	}
	if recipe.Timeout, err = boundedDuration("timeout", auth.Timeout, defaultLoginTimeout, minLoginTimeout, maxLoginTimeout); err != nil {
		return AuthRecipe{}, err
	}
	if recipe.StatusInterval, err = boundedDuration("status_interval", auth.StatusInterval, defaultStatusInterval, minStatusInterval, 24*time.Hour); err != nil {
		return AuthRecipe{}, err
	}
	if recipe.ExpiringWithin, err = boundedDuration("expiring_within", auth.ExpiringWithin, defaultExpiringWithin, 0, 90*24*time.Hour); err != nil {
		return AuthRecipe{}, err
	}
	return recipe, nil
}

func boundedDuration(field, value string, fallback, lower, upper time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", field, err)
	}
	if duration < lower || duration > upper {
		return 0, fmt.Errorf("%s must be between %s and %s", field, lower, upper)
	}
	return duration, nil
}
