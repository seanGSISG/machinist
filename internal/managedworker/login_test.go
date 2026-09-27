package managedworker

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

// fakeLoginScript behaves like a paste-back OAuth login: it prints a link as
// an OSC 8 hyperlink, a device code and a prompt, then checks the pasted code.
// It never talks to a real service.
const fakeLoginScript = `#!/bin/sh
printf 'cwd=%s home=%s path=%s leaked=%s\n' "$(pwd)" "$HOME" "$PATH" "${WORKER_SECRET:-none}" > "$HOME/env.txt"
printf 'Open this link: \033]8;;https://login.example.test/oauth?state=abc123\033\\sign in\033]8;;\033\\\n'
printf 'Your device code: \033[1mWXYZ-1234\033[0m\n'
printf 'Paste code here: '
read pasted
if [ "$pasted" = "sekrit-paste-value" ]; then
  touch "$HOME/.fake-credentials"
  echo "Logged in"
  exit 0
fi
echo "bad code"
exit 1
`

const fakeStatusScript = `#!/bin/sh
if [ -f "$HOME/.fake-credentials" ]; then echo '{"loggedIn": true, "expiresAt": 4102444800}'; exit 0; fi
echo '{"loggedIn": false}'
exit 1
`

func writeFakeCLI(t *testing.T, directory, name, script string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func testLoginEnvironment(t *testing.T) loginEnvironment {
	t.Helper()
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return loginEnvironment{home: t.TempDir(), user: account.Username, path: "/usr/bin:/bin"}
}

func fakeRecipe(t *testing.T, bin string) config.AuthRecipe {
	t.Helper()
	return config.AuthRecipe{
		Executor: "fake", Login: []string{"fake-login"}, Status: []string{"fake-status"}, Path: bin + ":/usr/bin:/bin",
		URL: regexp.MustCompile(`https://[^\s"'<>]+`), Code: regexp.MustCompile(`device code: ([A-Z0-9-]+)`), Prompt: regexp.MustCompile(`Paste code here`),
		Connected: regexp.MustCompile(`"loggedIn": true`), Timeout: time.Minute, StatusInterval: time.Minute, ExpiringWithin: 72 * time.Hour,
	}
}

func waitForReport(t *testing.T, session *loginSession, ready func(protocol.LoginSessionReport) bool) protocol.LoginSessionReport {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		session.mu.Lock()
		session.delivered = 0
		session.mu.Unlock()
		report, _, _ := session.report()
		if ready(report) {
			return report
		}
		if time.Now().After(deadline) {
			t.Fatalf("login report never became ready: %+v", report)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLoginSessionExtractsLinkAndCodeAndRedactsPastedInput(t *testing.T) {
	t.Setenv("WORKER_SECRET", "must-not-leak")
	bin := t.TempDir()
	writeFakeCLI(t, bin, "fake-login", fakeLoginScript)
	environment := testLoginEnvironment(t)
	session, err := startLogin("login_0123456789abcdef", fakeRecipe(t, bin), environment, nil)
	if err != nil {
		t.Fatal(err)
	}
	report := waitForReport(t, session, func(report protocol.LoginSessionReport) bool { return report.AwaitingInput })
	if report.State != protocol.LoginAwaiting || report.URL != "https://login.example.test/oauth?state=abc123" || report.Code != "WXYZ-1234" {
		t.Fatalf("awaiting report = %+v", report)
	}
	if err := session.write("sekrit-paste-value\r", true); err != nil {
		t.Fatal(err)
	}
	<-session.done
	report = waitForReport(t, session, func(report protocol.LoginSessionReport) bool { return report.State != protocol.LoginRunning })
	if report.State != protocol.LoginSucceeded || report.AwaitingInput {
		t.Fatalf("final report = %+v", report)
	}
	if strings.Contains(report.Transcript, "sekrit-paste-value") || strings.Contains(report.Transcript, "https://") || !strings.Contains(report.Transcript, "[redacted]") || !strings.Contains(report.Transcript, "Logged in") {
		t.Fatalf("transcript was not redacted:\n%s", report.Transcript)
	}
	recorded, err := os.ReadFile(filepath.Join(environment.home, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := "cwd=" + environment.home + " home=" + environment.home + " path=" + bin + ":/usr/bin:/bin leaked=none\n"
	if string(recorded) != want {
		t.Fatalf("login environment = %q, want %q", recorded, want)
	}
}

func TestLoginSessionTimeoutAndCancelKillTheTerminal(t *testing.T) {
	bin := t.TempDir()
	writeFakeCLI(t, bin, "fake-login", "#!/bin/sh\necho waiting\nsleep 30\n")
	for _, reason := range []string{protocol.LoginTimedOut, protocol.LoginCancelled} {
		t.Run(reason, func(t *testing.T) {
			recipe := fakeRecipe(t, bin)
			if reason == protocol.LoginTimedOut {
				recipe.Timeout = 300 * time.Millisecond
			}
			session, err := startLogin("login_0123456789abcdef", recipe, testLoginEnvironment(t), nil)
			if err != nil {
				t.Fatal(err)
			}
			if reason == protocol.LoginCancelled {
				waitForReport(t, session, func(report protocol.LoginSessionReport) bool { return strings.Contains(report.Transcript, "waiting") })
				session.stop(protocol.LoginCancelled)
			}
			select {
			case <-session.done:
			case <-time.After(5 * time.Second):
				t.Fatal("login process was not killed")
			}
			report, _, _ := session.report()
			if report.State != reason {
				t.Fatalf("state = %q, want %q", report.State, reason)
			}
			if err := session.write("late\r", true); err == nil {
				t.Fatal("finished session accepted input")
			}
		})
	}
}

func TestStatusCheckReportsConnectedExpiringExpiredAndUnknown(t *testing.T) {
	bin := t.TempDir()
	writeFakeCLI(t, bin, "fake-status", fakeStatusScript)
	environment := testLoginEnvironment(t)
	recipe := fakeRecipe(t, bin)
	now := func() time.Time { return time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC) }

	if state, detail, _ := checkStatus(context.Background(), recipe, environment, now); state != protocol.AuthExpired || !strings.Contains(detail, "code 1") {
		t.Fatalf("logged out status = %q %q", state, detail)
	}
	if err := os.WriteFile(filepath.Join(environment.home, ".fake-credentials"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := checkStatus(context.Background(), recipe, environment, now); state != protocol.AuthConnected {
		t.Fatalf("logged in status = %q", state)
	}
	recipe.Expires = regexp.MustCompile(`"expiresAt": (\d+)`)
	if state, _, expires := checkStatus(context.Background(), recipe, environment, now); state != protocol.AuthConnected || expires == nil || expires.Year() != 2100 {
		t.Fatalf("far expiry status = %q %v", state, expires)
	}
	nearExpiry := func() time.Time { return time.Unix(4102444800, 0).Add(-time.Hour) }
	if state, _, _ := checkStatus(context.Background(), recipe, environment, nearExpiry); state != protocol.AuthExpiring {
		t.Fatalf("near expiry status = %q", state)
	}
	pastExpiry := func() time.Time { return time.Unix(4102444800, 0).Add(time.Hour) }
	if state, _, _ := checkStatus(context.Background(), recipe, environment, pastExpiry); state != protocol.AuthExpired {
		t.Fatalf("past expiry status = %q", state)
	}
	// A script whose interpreter is not on the auth path (the node pitfall).
	writeFakeCLI(t, bin, "needs-runtime", "#!/usr/bin/env machinist-missing-runtime\n")
	recipe.Status = []string{"needs-runtime"}
	if state, detail, _ := checkStatus(context.Background(), recipe, environment, now); state != protocol.AuthUnknown || !strings.Contains(detail, "code 127") || !strings.Contains(detail, "path") {
		t.Fatalf("missing runtime status = %q %q", state, detail)
	}
	recipe.Status = []string{"missing-status-command"}
	if state, detail, _ := checkStatus(context.Background(), recipe, environment, now); state != protocol.AuthUnknown || !strings.Contains(detail, "not found") {
		t.Fatalf("missing command status = %q %q", state, detail)
	}
}

func TestRedactTranscriptKeepsOnlyARedactedTail(t *testing.T) {
	raw := "token sk-ant-" + strings.Repeat("a", 40) + "\r\nvisit https://example.test/x?code=1\r\ntyped hunter22\r\n" + strings.Repeat("line\r\n", 2000) + "done\r\n"
	transcript := redactTranscript(raw, []string{"hunter22"})
	if strings.Contains(transcript, "sk-ant-") || strings.Contains(transcript, "hunter22") || len(transcript) > maxTranscriptBytes || !strings.HasSuffix(transcript, "done") {
		t.Fatalf("transcript = %q", transcript[:min(len(transcript), 200)])
	}
	head := redactTranscript("token sk-ant-"+strings.Repeat("a", 40)+"\nvisit https://example.test/x\ntyped hunter22\n", []string{"hunter22"})
	if head != "token [redacted]\nvisit [login link]\ntyped [redacted]" {
		t.Fatalf("redacted = %q", head)
	}
}

// A finished session's process id may be reused, so stop must not signal it.
func TestStopAfterTheLoginEndedSignalsNothing(t *testing.T) {
	bystander := exec.Command("sleep", "30")
	bystander.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := bystander.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = bystander.Wait(); close(exited) }()
	defer func() { _ = syscall.Kill(-bystander.Process.Pid, syscall.SIGKILL); <-exited }()
	session := &loginSession{id: "login_0123456789abcdef", state: protocol.LoginSucceeded, process: bystander.Process, done: make(chan struct{})}
	session.stop(protocol.LoginCancelled)
	select {
	case <-exited:
		t.Fatal("stop signalled the process group of a finished login")
	case <-time.After(200 * time.Millisecond):
	}
	if session.state != protocol.LoginSucceeded || session.reason != "" {
		t.Fatalf("finished session changed: %q %q", session.state, session.reason)
	}
}
