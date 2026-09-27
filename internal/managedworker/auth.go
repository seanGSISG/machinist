package managedworker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

const (
	authIdleInterval   = 2 * time.Second
	authActiveInterval = 500 * time.Millisecond
	statusTimeout      = 30 * time.Second
	maxStatusOutput    = 64 << 10
	maxLoginSessions   = 4
)

var loginSessionID = regexp.MustCompile(`^login_[0-9a-f]{16,64}$`)

// authAgent is the worker-local auth broker. It reports each executor's login
// state, and runs login recipes from worker.toml when the control plane asks
// for one by executor name. Credentials stay with the CLI on this machine.
type authAgent struct {
	client      *Client
	instanceID  string
	name        string
	recipes     map[string]config.AuthRecipe
	environment loginEnvironment
	stderr      io.Writer
	now         func() time.Time

	mu        sync.Mutex
	statuses  map[string]protocol.ExecutorAuthReport
	nextCheck map[string]time.Time
	checking  map[string]bool
	sessions  map[string]*loginSession
	wake      chan struct{}
	lastError string
}

func newAuthAgent(workerConfig config.Worker, client *Client, instanceID string, stderr io.Writer) (*authAgent, error) {
	recipes := workerConfig.AuthRecipes()
	if len(recipes) == 0 {
		return nil, nil
	}
	environment, err := workerLoginEnvironment()
	if err != nil {
		return nil, err
	}
	agent := &authAgent{
		client: client, instanceID: instanceID, name: workerConfig.Name, recipes: recipes,
		environment: environment, stderr: stderr, now: time.Now,
		statuses: map[string]protocol.ExecutorAuthReport{}, nextCheck: map[string]time.Time{},
		checking: map[string]bool{}, sessions: map[string]*loginSession{}, wake: make(chan struct{}, 1),
	}
	for name, recipe := range recipes {
		agent.statuses[name] = protocol.ExecutorAuthReport{Login: len(recipe.Login) > 0, LoginTimeout: recipe.Timeout.Milliseconds(), State: protocol.AuthUnknown}
	}
	return agent, nil
}

// workerLoginEnvironment uses the worker account's home directory as the
// login working directory, so CLIs never read another user's project settings.
func workerLoginEnvironment() (loginEnvironment, error) {
	account, err := user.Current()
	if err != nil {
		return loginEnvironment{}, fmt.Errorf("find worker account: %w", err)
	}
	if account.HomeDir == "" {
		return loginEnvironment{}, errors.New("find worker account: home directory is empty")
	}
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	return loginEnvironment{home: account.HomeDir, user: account.Username, path: path, lang: os.Getenv("LANG")}, nil
}

func (a *authAgent) run(ctx context.Context) {
	defer a.stopAll()
	for {
		a.startDueChecks(ctx)
		a.sync(ctx)
		interval := authIdleInterval
		if a.active() {
			interval = authActiveInterval
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-a.wake:
			timer.Stop()
			// Coalesce bursts of terminal output into one report.
			if !wait(ctx, 150*time.Millisecond) {
				return
			}
		case <-timer.C:
		}
	}
}

func (a *authAgent) notify() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *authAgent) sync(ctx context.Context) {
	request, versions := a.snapshot()
	var response protocol.AuthSyncResponse
	if err := a.client.Post(ctx, "/api/v1/workers/auth", request, &response); err != nil {
		if ctx.Err() == nil && err.Error() != a.lastError {
			fmt.Fprintf(a.stderr, "machinist: auth sync: %v\n", err)
		}
		a.lastError = err.Error()
		return
	}
	a.lastError = ""
	a.delivered(versions)
	for _, action := range response.Actions {
		a.handle(ctx, action)
	}
}

func (a *authAgent) snapshot() (protocol.AuthSyncRequest, map[string]int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	request := protocol.AuthSyncRequest{InstanceID: a.instanceID, Name: a.name, Executors: make(map[string]protocol.ExecutorAuthReport, len(a.statuses))}
	for name, status := range a.statuses {
		request.Executors[name] = status
	}
	versions := map[string]int{}
	for id, session := range a.sessions {
		session.mu.Lock()
		if !session.ended() {
			request.Active = append(request.Active, id)
		}
		session.mu.Unlock()
		if report, version, changed := session.report(); changed {
			request.Sessions = append(request.Sessions, report)
			versions[id] = version
		}
	}
	return request, versions
}

func (a *authAgent) delivered(versions map[string]int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, version := range versions {
		if session := a.sessions[id]; session != nil && session.markDelivered(version) {
			delete(a.sessions, id)
		}
	}
}

func (a *authAgent) active() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.sessions) > 0
}

// handle applies one control plane action. The action names an executor;
// the argv always comes from this worker's own recipe.
func (a *authAgent) handle(ctx context.Context, action protocol.LoginAction) {
	if !loginSessionID.MatchString(action.SessionID) {
		return
	}
	a.mu.Lock()
	session := a.sessions[action.SessionID]
	a.mu.Unlock()
	switch action.Kind {
	case protocol.LoginActionStart:
		if session != nil {
			return
		}
		a.start(ctx, action)
	case protocol.LoginActionInput:
		if session == nil {
			return
		}
		text := action.Text
		if action.Key != "" {
			sequence, ok := protocol.LoginKeys[action.Key]
			if !ok {
				return
			}
			text = sequence
		} else {
			if len(text) > maxLoginInputBytes || strings.ContainsAny(text, "\x00\r\n\x1b") {
				return
			}
			text += "\r"
		}
		if err := session.write(text, action.Key == ""); err != nil {
			fmt.Fprintf(a.stderr, "machinist: login %s for %s: input: %v\n", session.id, session.executor, err)
		}
		a.notify()
	case protocol.LoginActionCancel:
		if session == nil {
			a.reject(action.SessionID, action.Executor, protocol.LoginCancelled, "")
			return
		}
		session.stop(protocol.LoginCancelled)
	}
}

func (a *authAgent) start(ctx context.Context, action protocol.LoginAction) {
	recipe, ok := a.recipes[action.Executor]
	if !ok || len(recipe.Login) == 0 {
		a.reject(action.SessionID, action.Executor, protocol.LoginFailed, "this worker has no login recipe for the executor")
		return
	}
	a.mu.Lock()
	busy := len(a.sessions) >= maxLoginSessions
	for _, existing := range a.sessions {
		existing.mu.Lock()
		if existing.executor == action.Executor && !existing.ended() {
			busy = true
		}
		existing.mu.Unlock()
	}
	a.mu.Unlock()
	if busy || ctx.Err() != nil {
		a.reject(action.SessionID, action.Executor, protocol.LoginFailed, "another login is already running on this worker")
		return
	}
	session, err := startLogin(action.SessionID, recipe, a.environment, a.notify)
	if err != nil {
		a.reject(action.SessionID, action.Executor, protocol.LoginFailed, err.Error())
		return
	}
	fmt.Fprintf(a.stderr, "machinist: login %s for %s: started\n", session.id, session.executor)
	a.mu.Lock()
	a.sessions[session.id] = session
	a.mu.Unlock()
	go func() {
		<-session.done
		session.mu.Lock()
		state := session.state
		session.mu.Unlock()
		fmt.Fprintf(a.stderr, "machinist: login %s for %s: %s\n", session.id, session.executor, state)
		// Re-check right away so the new credentials show up.
		a.mu.Lock()
		a.nextCheck[session.executor] = time.Time{}
		a.mu.Unlock()
		a.notify()
	}()
	a.notify()
}

// reject reports a session that never started (or is unknown) as finished.
func (a *authAgent) reject(id, executor, state, message string) {
	session := &loginSession{id: id, executor: executor, state: state, err: message, version: 1, done: make(chan struct{})}
	close(session.done)
	a.mu.Lock()
	a.sessions[id] = session
	a.mu.Unlock()
	a.notify()
}

func (a *authAgent) stopAll() {
	a.mu.Lock()
	sessions := make([]*loginSession, 0, len(a.sessions))
	for _, session := range a.sessions {
		sessions = append(sessions, session)
	}
	a.mu.Unlock()
	for _, session := range sessions {
		session.stop(protocol.LoginCancelled)
	}
}

func (a *authAgent) startDueChecks(ctx context.Context) {
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for name, recipe := range a.recipes {
		if len(recipe.Status) == 0 || a.checking[name] || now.Before(a.nextCheck[name]) {
			continue
		}
		a.checking[name] = true
		go func() {
			state, detail, expires := checkStatus(ctx, recipe, a.environment, a.now)
			checked := a.now().UTC()
			a.mu.Lock()
			status := a.statuses[name]
			status.State, status.Detail, status.ExpiresAt, status.CheckedAt = state, detail, expires, &checked
			a.statuses[name] = status
			a.checking[name] = false
			a.nextCheck[name] = a.now().Add(recipe.StatusInterval)
			a.mu.Unlock()
			a.notify()
		}()
	}
}

// checkStatus runs the status argv without a terminal. Its output is parsed
// here and discarded; only the state and a short detail are reported.
func checkStatus(ctx context.Context, recipe config.AuthRecipe, environment loginEnvironment, now func() time.Time) (string, string, *time.Time) {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	command, err := environment.command(ctx, recipe, recipe.Status)
	if err != nil {
		return protocol.AuthUnknown, "status check could not start: " + err.Error(), nil
	}
	var output limitedBuffer
	command.Stdout, command.Stderr = &output, &output
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = time.Second
	err = command.Run()
	if ctx.Err() != nil {
		return protocol.AuthUnknown, "status check timed out", nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return protocol.AuthExpired, fmt.Sprintf("status check exited with code %d", exitErr.ExitCode()), nil
	}
	if err != nil {
		return protocol.AuthUnknown, "status check failed: " + err.Error(), nil
	}
	text := plainText(output.String())
	if recipe.Connected != nil && !recipe.Connected.MatchString(text) {
		return protocol.AuthExpired, "status check did not report a login", nil
	}
	if recipe.Expires != nil {
		match := recipe.Expires.FindStringSubmatch(text)
		if match == nil {
			return protocol.AuthConnected, "", nil
		}
		expires, ok := parseExpiry(match[1])
		if !ok {
			return protocol.AuthConnected, "expiry time not recognized", nil
		}
		remaining := expires.Sub(now())
		switch {
		case remaining <= 0:
			return protocol.AuthExpired, "login expired", &expires
		case remaining <= recipe.ExpiringWithin:
			return protocol.AuthExpiring, "login expires soon", &expires
		}
		return protocol.AuthConnected, "", &expires
	}
	return protocol.AuthConnected, "", nil
}

func parseExpiry(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC(), true
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds > 1e12 {
			return time.UnixMilli(seconds).UTC(), true
		}
		return time.Unix(seconds, 0).UTC(), true
	}
	return time.Time{}, false
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(chunk []byte) (int, error) {
	if room := maxStatusOutput - b.Len(); room > 0 {
		b.Buffer.Write(chunk[:min(len(chunk), room)])
	}
	return len(chunk), nil
}
