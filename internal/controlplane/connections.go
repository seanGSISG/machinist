package controlplane

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/machinist/internal/protocol"
)

const (
	loginPickupWindow   = 30 * time.Second
	loginWorkerSilence  = 30 * time.Second
	loginDeadlineGrace  = 30 * time.Second
	loginRetention      = 10 * time.Minute
	defaultLoginTimeout = 10 * time.Minute
	maxLoginTranscript  = 4 << 10
	maxLoginURL         = 4 << 10
	maxLoginCode        = 64
	maxLoginInput       = 4 << 10
	// maxQueuedInputs bounds the input waiting for one session's next worker
	// sync, so a client cannot grow control plane memory or the sync response.
	maxQueuedInputs = 16
)

var (
	ErrLoginNotFound = errors.New("login session not found")
	ErrLoginActive   = errors.New("a login is already in progress for this executor")
	ErrLoginEnded    = errors.New("login session has ended")
	ErrLoginBusy     = errors.New("too much input is waiting for the worker; try again in a moment")
)

// loginBroker relays login sessions between the web UI and workers. It keeps
// everything in memory: device codes, login links and pasted input are never
// written to the database or logs, and a restart ends every session.
type loginBroker struct {
	mu       sync.Mutex
	now      func() time.Time
	sessions map[string]*loginSession
	// pending holds queued actions by worker instance, not name: during a
	// restart two instances can share a name, and only the instance the
	// session was started on may pick its actions up.
	pending map[string][]protocol.LoginAction
}

type loginSession struct {
	id         string
	worker     string
	instance   string
	executor   string
	tokenHash  [32]byte
	state      string
	url        string
	code       string
	awaiting   bool
	transcript string
	err        string
	createdAt  time.Time
	deadline   time.Time
	lastReport time.Time
	endedAt    time.Time
}

// LoginSessionView is what the requesting UI sees. Only the holder of the
// session token can read it.
type LoginSessionView struct {
	ID            string    `json:"id"`
	Worker        string    `json:"worker"`
	Executor      string    `json:"executor"`
	State         string    `json:"state"`
	URL           string    `json:"url,omitempty"`
	Code          string    `json:"code,omitempty"`
	AwaitingInput bool      `json:"awaiting_input"`
	Transcript    string    `json:"transcript,omitempty"`
	Error         string    `json:"error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	Deadline      time.Time `json:"deadline"`
}

// LoginSessionSummary is the non-sensitive part of an active session.
type LoginSessionSummary struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
}

func newLoginBroker(now func() time.Time) *loginBroker {
	return &loginBroker{now: now, sessions: map[string]*loginSession{}, pending: map[string][]protocol.LoginAction{}}
}

func loginEnded(state string) bool {
	return state != protocol.LoginPending && state != protocol.LoginRunning && state != protocol.LoginAwaiting
}

// start creates a single-use session on one worker instance and returns its
// bearer token. With replace, an active session for the same worker name and
// executor is cancelled first.
func (b *loginBroker) start(worker, instance, executor string, timeout time.Duration, replace bool) (LoginSessionView, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expireLocked()
	for _, session := range b.sessions {
		if session.worker == worker && session.executor == executor && !loginEnded(session.state) {
			if !replace {
				return LoginSessionView{}, "", ErrLoginActive
			}
			b.cancelLocked(session)
		}
	}
	id, err := randomID("login", 16)
	if err != nil {
		return LoginSessionView{}, "", err
	}
	token, err := randomID("logintoken", 32)
	if err != nil {
		return LoginSessionView{}, "", err
	}
	if timeout <= 0 {
		timeout = defaultLoginTimeout
	}
	now := b.now().UTC()
	session := &loginSession{id: id, worker: worker, instance: instance, executor: executor, tokenHash: sha256.Sum256([]byte(token)), state: protocol.LoginPending, createdAt: now, deadline: now.Add(timeout + loginDeadlineGrace)}
	b.sessions[id] = session
	b.pending[instance] = append(b.pending[instance], protocol.LoginAction{SessionID: id, Kind: protocol.LoginActionStart, Executor: executor})
	return session.view(), token, nil
}

// authorized returns the session only for the token issued with it.
func (b *loginBroker) authorized(id, token string) (*loginSession, error) {
	session := b.sessions[id]
	provided := sha256.Sum256([]byte(token))
	if session == nil || token == "" || subtle.ConstantTimeCompare(provided[:], session.tokenHash[:]) != 1 {
		return nil, ErrLoginNotFound
	}
	return session, nil
}

func (b *loginBroker) get(id, token string) (LoginSessionView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expireLocked()
	session, err := b.authorized(id, token)
	if err != nil {
		return LoginSessionView{}, err
	}
	return session.view(), nil
}

// input queues text or a key for the worker. It is held in memory only until
// the worker's next sync.
func (b *loginBroker) input(id, token, text, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expireLocked()
	session, err := b.authorized(id, token)
	if err != nil {
		return err
	}
	if session.state != protocol.LoginRunning && session.state != protocol.LoginAwaiting {
		if loginEnded(session.state) {
			return ErrLoginEnded
		}
		return errors.New("the worker has not started this login yet")
	}
	if key != "" {
		if _, ok := protocol.LoginKeys[key]; !ok || text != "" {
			return errors.New("choose text or one of enter, up, down, tab, escape")
		}
	} else if len(text) > maxLoginInput || strings.ContainsAny(text, "\x00\r\n\x1b") {
		return errors.New("input must be one line of at most 4 KiB")
	}
	queued := 0
	for _, action := range b.pending[session.instance] {
		if action.SessionID == id && action.Kind == protocol.LoginActionInput {
			queued++
		}
	}
	if queued >= maxQueuedInputs {
		return ErrLoginBusy
	}
	session.awaiting = false
	if session.state == protocol.LoginAwaiting {
		session.state = protocol.LoginRunning
	}
	b.pending[session.instance] = append(b.pending[session.instance], protocol.LoginAction{SessionID: id, Kind: protocol.LoginActionInput, Text: text, Key: key})
	return nil
}

func (b *loginBroker) cancel(id, token string) (LoginSessionView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expireLocked()
	session, err := b.authorized(id, token)
	if err != nil {
		return LoginSessionView{}, err
	}
	if loginEnded(session.state) {
		return session.view(), ErrLoginEnded
	}
	b.cancelLocked(session)
	return session.view(), nil
}

func (b *loginBroker) cancelLocked(session *loginSession) {
	started := session.state != protocol.LoginPending
	b.endLocked(session, protocol.LoginCancelled, "")
	if started {
		b.pending[session.instance] = append(b.pending[session.instance], protocol.LoginAction{SessionID: session.id, Kind: protocol.LoginActionCancel, Executor: session.executor})
	}
}

// endLocked makes a session terminal and drops its queued start and input.
func (b *loginBroker) endLocked(session *loginSession, state, message string) {
	session.state, session.awaiting = state, false
	if message != "" {
		session.err = message
	}
	session.endedAt = b.now().UTC()
	actions := b.pending[session.instance][:0]
	for _, action := range b.pending[session.instance] {
		if action.SessionID != session.id || action.Kind == protocol.LoginActionCancel {
			actions = append(actions, action)
		}
	}
	b.pending[session.instance] = actions
}

// dropActionsLocked forgets every queued action for a session, so an
// instance that never syncs again does not keep them forever.
func (b *loginBroker) dropActionsLocked(instance, id string) {
	actions := b.pending[instance][:0]
	for _, action := range b.pending[instance] {
		if action.SessionID != id {
			actions = append(actions, action)
		}
	}
	if len(actions) == 0 {
		delete(b.pending, instance)
		return
	}
	b.pending[instance] = actions
}

// sync applies a worker instance's session reports and hands it its queued
// actions. An instance can only update sessions started on it.
func (b *loginBroker) sync(instance string, reports []protocol.LoginSessionReport, active []string) []protocol.LoginAction {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now().UTC()
	for _, report := range reports {
		session := b.sessions[report.ID]
		if session == nil || session.instance != instance || loginEnded(session.state) {
			continue
		}
		session.lastReport = now
		session.url = ""
		if validLoginURL(report.URL) {
			session.url = report.URL
		}
		session.code = ""
		if len(report.Code) <= maxLoginCode && !strings.ContainsAny(report.Code, "\x00\r\n") {
			session.code = report.Code
		}
		session.transcript = report.Transcript
		if len(session.transcript) > maxLoginTranscript {
			session.transcript = session.transcript[len(session.transcript)-maxLoginTranscript:]
		}
		switch report.State {
		case protocol.LoginRunning, protocol.LoginAwaiting:
			session.state = report.State
			session.awaiting = report.AwaitingInput
		case protocol.LoginSucceeded, protocol.LoginFailed, protocol.LoginCancelled, protocol.LoginTimedOut:
			b.endLocked(session, report.State, truncateUTF8(report.Error, maxAuthDetailBytes))
		}
	}
	for _, id := range active {
		if session := b.sessions[id]; session != nil && session.instance == instance {
			session.lastReport = now
		}
	}
	b.expireLocked()
	actions := b.pending[instance]
	delete(b.pending, instance)
	for _, action := range actions {
		if action.Kind == protocol.LoginActionStart {
			if session := b.sessions[action.SessionID]; session != nil {
				session.state, session.lastReport = protocol.LoginRunning, now
			}
		}
	}
	return actions
}

// expireLocked fails sessions a worker never started or stopped reporting,
// and forgets finished sessions after a while.
func (b *loginBroker) expireLocked() {
	now := b.now().UTC()
	for id, session := range b.sessions {
		switch {
		case loginEnded(session.state):
			if now.Sub(session.endedAt) > loginRetention {
				delete(b.sessions, id)
				b.dropActionsLocked(session.instance, id)
			}
		case session.state == protocol.LoginPending && now.Sub(session.createdAt) > loginPickupWindow:
			b.endLocked(session, protocol.LoginFailed, "the worker did not start the login; is it online?")
		case now.After(session.deadline):
			b.endLocked(session, protocol.LoginTimedOut, "")
			b.pending[session.instance] = append(b.pending[session.instance], protocol.LoginAction{SessionID: id, Kind: protocol.LoginActionCancel, Executor: session.executor})
		case session.state != protocol.LoginPending && now.Sub(session.lastReport) > loginWorkerSilence:
			// The worker may only be disconnected: stop its terminal if it comes back.
			b.endLocked(session, protocol.LoginFailed, "the worker stopped responding")
			b.pending[session.instance] = append(b.pending[session.instance], protocol.LoginAction{SessionID: id, Kind: protocol.LoginActionCancel, Executor: session.executor})
		}
	}
}

// active summarizes the unfinished session for each worker and executor.
func (b *loginBroker) active() map[string]LoginSessionSummary {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expireLocked()
	summaries := map[string]LoginSessionSummary{}
	for _, session := range b.sessions {
		if !loginEnded(session.state) {
			summaries[session.worker+"\x00"+session.executor] = LoginSessionSummary{ID: session.id, State: session.state, CreatedAt: session.createdAt}
		}
	}
	return summaries
}

func (s *loginSession) view() LoginSessionView {
	return LoginSessionView{ID: s.id, Worker: s.worker, Executor: s.executor, State: s.state, URL: s.url, Code: s.code, AwaitingInput: s.awaiting, Transcript: s.transcript, Error: s.err, CreatedAt: s.createdAt, Deadline: s.deadline}
}

// validLoginURL accepts only https links, so the UI never renders a
// javascript: or data: link reported by a worker.
func validLoginURL(raw string) bool {
	if raw == "" || len(raw) > maxLoginURL {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

// truncateUTF8 cuts value to at most limit bytes without splitting a rune.
func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
