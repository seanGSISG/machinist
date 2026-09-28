package protocol

import "time"

// Auth states reported by a worker's status check.
const (
	AuthConnected = "connected"
	AuthExpiring  = "expiring"
	AuthExpired   = "expired"
	AuthUnknown   = "unknown"
)

// Login session states. A session starts pending on the control plane and
// ends in exactly one terminal state.
const (
	LoginPending   = "pending"
	LoginRunning   = "running"
	LoginAwaiting  = "awaiting_input"
	LoginSucceeded = "succeeded"
	LoginFailed    = "failed"
	LoginCancelled = "cancelled"
	LoginTimedOut  = "timed_out"
)

// Login actions sent from the control plane to a worker.
const (
	LoginActionStart  = "start"
	LoginActionInput  = "input"
	LoginActionCancel = "cancel"
)

// Keys the web UI can send to a login terminal in addition to text.
var LoginKeys = map[string]string{
	"enter":  "\r",
	"up":     "\x1b[A",
	"down":   "\x1b[B",
	"tab":    "\t",
	"escape": "\x1b",
}

// AuthSyncRequest reports a worker's auth state and login progress. It never
// carries credentials: only states, the login URL, a device code the user
// must type elsewhere, and a redacted terminal tail.
type AuthSyncRequest struct {
	InstanceID string                        `json:"instance_id"`
	Name       string                        `json:"name"`
	Executors  map[string]ExecutorAuthReport `json:"executors"`
	Sessions   []LoginSessionReport          `json:"sessions,omitempty"`
	// Active lists every login session still running on this worker.
	Active []string `json:"active_sessions,omitempty"`
}

// ExecutorAuthReport describes one executor that has an auth recipe.
type ExecutorAuthReport struct {
	Login        bool       `json:"login"`
	LoginTimeout int64      `json:"login_timeout_millis,omitempty"`
	State        string     `json:"state"`
	Detail       string     `json:"detail,omitempty"`
	CheckedAt    *time.Time `json:"checked_at,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

// LoginSessionReport is the latest state of one login session on the worker.
type LoginSessionReport struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	URL           string `json:"url,omitempty"`
	Code          string `json:"code,omitempty"`
	AwaitingInput bool   `json:"awaiting_input,omitempty"`
	Transcript    string `json:"transcript,omitempty"`
	Error         string `json:"error,omitempty"`
}

// AuthSyncResponse carries pending login actions for the worker.
type AuthSyncResponse struct {
	Actions   []LoginAction        `json:"actions,omitempty"`
	RecheckAt map[string]time.Time `json:"recheck_at,omitempty"`
}

// LoginAction names an executor, never a command: the worker runs only the
// login argv from its own worker.toml.
type LoginAction struct {
	SessionID string `json:"session_id"`
	Kind      string `json:"kind"`
	Executor  string `json:"executor,omitempty"`
	Text      string `json:"text,omitempty"`
	Key       string `json:"key,omitempty"`
}
