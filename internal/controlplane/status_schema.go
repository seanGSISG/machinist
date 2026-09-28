package controlplane

import "time"

const statusSchemaVersion = 1

type StatusResponse struct {
	SchemaVersion         int                  `json:"schema_version"`
	GeneratedAt           time.Time            `json:"generated_at"`
	Jobs                  []JobSummary         `json:"jobs"`
	Workers               []Worker             `json:"workers"`
	Triggers              []TriggerStatus      `json:"triggers"`
	Workflows             []string             `json:"workflows"`
	Commands              []string             `json:"commands"`
	Repositories          []string             `json:"repositories"`
	Connections           []ExecutorAuthStatus `json:"connections"`
	Executors             []ExecutorStatus     `json:"executors"`
	GatesAwaitingApproval []StatusItem         `json:"gates_awaiting_approval"`
	BlockedJobs           []StatusItem         `json:"blocked_jobs"`
	Logins                []StatusItem         `json:"logins"`
	CSRFToken             string               `json:"csrf_token"`
}

// statusResponse remains the package-local spelling used by older tests and
// callers while StatusResponse is the published v-schema type.
type statusResponse = StatusResponse

type ExecutorStatus struct {
	ID               string     `json:"id"`
	Worker           string     `json:"worker"`
	Executor         string     `json:"executor"`
	State            string     `json:"state"`
	Since            time.Time  `json:"since"`
	Reason           string     `json:"reason"`
	Message          string     `json:"message"`
	RateLimitedUntil *time.Time `json:"rate_limited_until"`
	ResetSource      *string    `json:"reset_source"`
}

type StatusItem struct {
	ID      string    `json:"id"`
	State   string    `json:"state"`
	Since   time.Time `json:"since"`
	Reason  string    `json:"reason"`
	Message string    `json:"message"`
	HTMLURL string    `json:"html_url"`
}
