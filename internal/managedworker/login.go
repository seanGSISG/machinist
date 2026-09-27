package managedworker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

const (
	maxLoginOutputBytes = 64 << 10
	maxTranscriptBytes  = 4 << 10
	maxLoginURLBytes    = 4 << 10
	maxLoginCodeBytes   = 64
	maxLoginInputBytes  = 4 << 10
	loginTerminalRows   = 50
	// Wide enough that long OAuth URLs are not wrapped across lines.
	loginTerminalCols = 400
)

var (
	osc8Link      = regexp.MustCompile(`\x1b\]8;[^;\x07\x1b]*;([^\x07\x1b]*)(?:\x07|\x1b\\)`)
	cursorForward = regexp.MustCompile(`\x1b\[[0-9]*C`)
	ansiSequence  = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]|\x1b.`)
	anyURL        = regexp.MustCompile(`https?://[^\s"'<>]+`)
	tokenLike     = regexp.MustCompile(`[A-Za-z0-9_\-+/=.~]{32,}`)
)

// loginSession is one login process in a pseudo-terminal. Its output stays in
// memory on the worker; only the extracted URL and device code and a redacted
// tail of the terminal are reported.
type loginSession struct {
	id       string
	executor string
	recipe   config.AuthRecipe

	mu         sync.Mutex
	terminal   *os.File
	process    *os.Process
	output     []byte
	inputStart int
	secrets    []string
	state      string
	url        string
	code       string
	awaiting   bool
	err        string
	reason     string
	version    int
	delivered  int
	done       chan struct{}
	changed    func()
}

type loginEnvironment struct {
	home string
	user string
	path string
	lang string
}

// environ is the entire environment of an auth command: `env -i` plus an
// explicit HOME and PATH, so the CLI never reads the operator's settings or
// inherits worker secrets.
func (e loginEnvironment) environ(recipe config.AuthRecipe) []string {
	path := e.path
	if recipe.Path != "" {
		path = recipe.Path
	}
	lang := e.lang
	if lang == "" {
		lang = "C.UTF-8"
	}
	return []string{"HOME=" + e.home, "USER=" + e.user, "LOGNAME=" + e.user, "PATH=" + path, "TERM=xterm-256color", "LANG=" + lang}
}

func (e loginEnvironment) command(ctx context.Context, recipe config.AuthRecipe, argv []string) (*exec.Cmd, error) {
	environment := e.environ(recipe)
	executable, err := lookPathIn(argv[0], strings.TrimPrefix(environment[3], "PATH="))
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, executable, argv[1:]...)
	command.Args[0] = argv[0]
	command.Dir = e.home
	command.Env = environment
	return command, nil
}

// lookPathIn resolves an executable against the auth PATH rather than the
// worker's own PATH.
func lookPathIn(name, path string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	for _, directory := range strings.Split(path, ":") {
		if directory == "" {
			continue
		}
		candidate := directory + "/" + name
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s: executable not found in PATH", name)
}

func startLogin(id string, recipe config.AuthRecipe, environment loginEnvironment, changed func()) (*loginSession, error) {
	command, err := environment.command(context.Background(), recipe, recipe.Login)
	if err != nil {
		return nil, err
	}
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: loginTerminalRows, Cols: loginTerminalCols})
	if err != nil {
		return nil, fmt.Errorf("start login: %w", err)
	}
	session := &loginSession{id: id, executor: recipe.Executor, recipe: recipe, terminal: terminal, process: command.Process, state: protocol.LoginRunning, version: 1, done: make(chan struct{}), changed: changed}
	readerDone := make(chan struct{})
	go session.read(readerDone)
	timer := time.AfterFunc(recipe.Timeout, func() { session.stop(protocol.LoginTimedOut) })
	go func() {
		waitErr := command.Wait()
		timer.Stop()
		// The login may leave children holding the terminal open.
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		select {
		case <-readerDone:
		case <-time.After(2 * time.Second):
		}
		_ = terminal.Close()
		session.finish(waitErr)
	}()
	if len(recipe.StartInput) > 0 {
		go func() {
			for _, input := range recipe.StartInput {
				select {
				case <-session.done:
					return
				case <-time.After(time.Second):
				}
				if session.write(input, false) != nil {
					return
				}
			}
		}()
	}
	return session, nil
}

func (s *loginSession) read(done chan<- struct{}) {
	defer close(done)
	buffer := make([]byte, 4096)
	for {
		count, err := s.terminal.Read(buffer)
		if count > 0 {
			s.appendOutput(buffer[:count])
		}
		if err != nil {
			return
		}
	}
}

func (s *loginSession) appendOutput(chunk []byte) {
	s.mu.Lock()
	s.output = append(s.output, chunk...)
	if excess := len(s.output) - maxLoginOutputBytes; excess > 0 {
		s.output = append([]byte(nil), s.output[excess:]...)
		s.inputStart = max(0, s.inputStart-excess)
	}
	s.extract()
	s.version++
	changed := s.changed
	s.mu.Unlock()
	if changed != nil {
		changed()
	}
}

// extract finds the latest login URL, device code and paste prompt. Callers hold mu.
func (s *loginSession) extract() {
	text := plainText(string(s.output))
	for _, match := range reverse(s.recipe.URL.FindAllString(text, -1)) {
		if candidate := strings.TrimRight(match, ".,;:)]}"); validLoginURL(candidate) {
			s.url = candidate
			break
		}
	}
	if s.recipe.Code != nil {
		if matches := s.recipe.Code.FindAllStringSubmatch(text, -1); len(matches) > 0 {
			match := matches[len(matches)-1]
			code := match[0]
			if len(match) > 1 {
				code = match[1]
			}
			if code = strings.TrimSpace(code); code != "" && len(code) <= maxLoginCodeBytes && !containsSecret(code, s.secrets) {
				s.code = code
			}
		}
	}
	if s.recipe.Prompt != nil {
		s.awaiting = s.recipe.Prompt.MatchString(plainText(string(s.output[s.inputStart:])))
	}
}

func reverse(values []string) []string {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
	return values
}

func validLoginURL(raw string) bool {
	if len(raw) > maxLoginURLBytes {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

// write sends operator input to the login terminal. Text is remembered only
// to redact its echo from the transcript.
func (s *loginSession) write(text string, secret bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != protocol.LoginRunning {
		return errors.New("login is not running")
	}
	if secret && len(strings.TrimSpace(text)) >= 3 {
		s.secrets = append(s.secrets, strings.TrimSpace(text))
	}
	if _, err := io.WriteString(s.terminal, text); err != nil {
		return err
	}
	s.inputStart = len(s.output)
	s.awaiting = false
	s.version++
	return nil
}

func (s *loginSession) stop(reason string) {
	s.mu.Lock()
	if s.reason == "" && s.state == protocol.LoginRunning {
		s.reason = reason
	}
	process := s.process
	s.mu.Unlock()
	if process != nil {
		_ = syscall.Kill(-process.Pid, syscall.SIGKILL)
	}
}

func (s *loginSession) finish(waitErr error) {
	s.mu.Lock()
	switch {
	case s.reason != "":
		s.state = s.reason
	case waitErr == nil:
		s.state = protocol.LoginSucceeded
	default:
		s.state = protocol.LoginFailed
		s.err = "login exited: " + waitErr.Error()
	}
	s.awaiting = false
	s.version++
	changed := s.changed
	s.mu.Unlock()
	close(s.done)
	if changed != nil {
		changed()
	}
}

// ended reports whether the session reached a terminal state. Callers hold mu.
func (s *loginSession) ended() bool { return s.state != protocol.LoginRunning }

// report returns the session state for the control plane and the version it represents.
func (s *loginSession) report() (protocol.LoginSessionReport, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.version == s.delivered {
		return protocol.LoginSessionReport{}, 0, false
	}
	state := s.state
	if state == protocol.LoginRunning && s.awaiting {
		state = protocol.LoginAwaiting
	}
	return protocol.LoginSessionReport{ID: s.id, State: state, URL: s.url, Code: s.code, AwaitingInput: s.awaiting, Transcript: redactTranscript(string(s.output), s.secrets), Error: s.err}, s.version, true
}

func (s *loginSession) markDelivered(version int) (finished bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delivered = max(s.delivered, version)
	return s.ended() && s.delivered == s.version
}

// plainText removes terminal control sequences, keeping OSC 8 link targets.
func plainText(raw string) string {
	raw = osc8Link.ReplaceAllString(raw, " $1 ")
	raw = cursorForward.ReplaceAllString(raw, " ")
	raw = ansiSequence.ReplaceAllString(raw, "")
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= ' ' && r != 0x7f {
			return r
		}
		return -1
	}, raw)
}

// redactTranscript is the only form in which terminal output leaves the
// worker: links, anything typed into the terminal, and token-like strings are
// replaced, and only the tail is kept.
func redactTranscript(raw string, secrets []string) string {
	text := plainText(raw)
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, "[redacted]")
	}
	text = anyURL.ReplaceAllString(text, "[login link]")
	text = tokenLike.ReplaceAllString(text, "[redacted]")
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if line = strings.TrimRight(line, " \t"); line != "" {
			kept = append(kept, line)
		}
	}
	text = strings.Join(kept, "\n")
	if len(text) > maxTranscriptBytes {
		text = text[len(text)-maxTranscriptBytes:]
		if index := strings.IndexByte(text, '\n'); index >= 0 {
			text = text[index+1:]
		}
	}
	return text
}

func containsSecret(value string, secrets []string) bool {
	for _, secret := range secrets {
		if strings.Contains(value, secret) || strings.Contains(secret, value) {
			return true
		}
	}
	return false
}
