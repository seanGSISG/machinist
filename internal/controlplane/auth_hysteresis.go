package controlplane

import "time"

const (
	authStateOK         = "ok"
	authStateRechecking = "rechecking"
	authStateExpired    = "expired"
	authRecheckDelay    = 30 * time.Second
)

// CheckResult is the credential checker's credential-free result. A result
// with none of the definitive fields set is a soft failure.
type CheckResult struct {
	Success    bool
	StatusCode int
	LoggedOut  bool
	ExpiresAt  *time.Time
}

// authFSM prevents one inconclusive credential check from expiring a login.
// Callers inject both the checker result and the clock value into Observe.
type authFSM struct {
	state        string
	softFailures int
}

func (f *authFSM) Observe(result CheckResult, now time.Time) (state string, recheckAt time.Time) {
	if result.LoggedOut || result.StatusCode == 401 || (result.ExpiresAt != nil && !result.ExpiresAt.After(now)) {
		f.state = authStateExpired
		f.softFailures = 0
		return f.state, time.Time{}
	}
	if result.Success {
		f.state = authStateOK
		f.softFailures = 0
		return f.state, time.Time{}
	}

	f.softFailures++
	if f.softFailures >= 2 || f.state == authStateExpired {
		f.state = authStateExpired
		return f.state, time.Time{}
	}
	f.state = authStateRechecking
	return f.state, now.Add(authRecheckDelay)
}
