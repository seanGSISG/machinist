package controlplane

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

func TestAuthHysteresis(t *testing.T) {
	t.Run("state machine", func(t *testing.T) {
		tests := []struct {
			name         string
			observations func(time.Time) []CheckResult
			wantStates   []string
			wantRechecks []time.Duration
		}{
			{
				name: "first soft failure rechecks and second expires",
				observations: func(time.Time) []CheckResult {
					return []CheckResult{{Success: true}, {}, {}}
				},
				wantStates:   []string{authStateOK, authStateRechecking, authStateExpired},
				wantRechecks: []time.Duration{0, authRecheckDelay, 0},
			},
			{
				name: "success resets consecutive failures",
				observations: func(time.Time) []CheckResult {
					return []CheckResult{{}, {Success: true}, {}}
				},
				wantStates:   []string{authStateRechecking, authStateOK, authStateRechecking},
				wantRechecks: []time.Duration{authRecheckDelay, 0, authRecheckDelay},
			},
			{
				name: "past expiry expires immediately",
				observations: func(now time.Time) []CheckResult {
					expired := now.Add(-time.Second)
					return []CheckResult{{ExpiresAt: &expired}}
				},
				wantStates:   []string{authStateExpired},
				wantRechecks: []time.Duration{0},
			},
			{
				name: "401 expires immediately",
				observations: func(time.Time) []CheckResult {
					return []CheckResult{{StatusCode: 401}}
				},
				wantStates:   []string{authStateExpired},
				wantRechecks: []time.Duration{0},
			},
			{
				name: "logout expires immediately",
				observations: func(time.Time) []CheckResult {
					return []CheckResult{{LoggedOut: true}}
				},
				wantStates:   []string{authStateExpired},
				wantRechecks: []time.Duration{0},
			},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					now := time.Now()
					fsm := authFSM{state: authStateOK}
					for i, result := range test.observations(now) {
						state, recheckAt := fsm.Observe(result, now)
						if state != test.wantStates[i] {
							t.Errorf("observation %d state = %q, want %q", i, state, test.wantStates[i])
						}
						var delay time.Duration
						if !recheckAt.IsZero() {
							delay = recheckAt.Sub(now)
						}
						if delay != test.wantRechecks[i] {
							t.Errorf("observation %d recheck delay = %s, want %s", i, delay, test.wantRechecks[i])
						}
					}
				})
			})
		}
	})

	t.Run("state change event", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
			now := time.Now().UTC()
			store.now = func() time.Time { return now }
			request := protocol.AuthSyncRequest{
				InstanceID: "worker-1",
				Name:       "colo",
				Executors: map[string]protocol.ExecutorAuthReport{
					"claude": {State: protocol.AuthConnected, CheckedAt: &now},
				},
			}
			if err := store.RecordExecutorAuth(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(t.Context(), `DELETE FROM events`); err != nil {
				t.Fatal(err)
			}

			firstFailureAt := now.Add(time.Minute)
			request.Executors["claude"] = protocol.ExecutorAuthReport{
				State: protocol.AuthExpired, Detail: "status check exited with code 1", CheckedAt: &firstFailureAt,
			}
			if err := store.RecordExecutorAuth(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if err := store.RecordExecutorAuth(t.Context(), request); err != nil {
				t.Fatal(err)
			}

			var eventType, subjectKind, subjectID, payloadJSON string
			if err := store.db.QueryRowContext(t.Context(), `SELECT type,subject_kind,subject_id,payload_json FROM events`).Scan(
				&eventType, &subjectKind, &subjectID, &payloadJSON,
			); err != nil {
				t.Fatal(err)
			}
			var payload map[string]string
			if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
				t.Fatal(err)
			}
			if eventType != "auth_change" || subjectKind != "executor" || subjectID != "colo/claude" ||
				payload["from"] != authStateOK || payload["to"] != authStateRechecking {
				t.Fatalf("event = %q %q %q %s", eventType, subjectKind, subjectID, payloadJSON)
			}
			var state string
			var events int
			if err := store.db.QueryRowContext(t.Context(), `SELECT state FROM worker_executor_auth WHERE worker_instance='worker-1' AND executor='claude'`).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if state != protocol.AuthUnknown || events != 1 {
				t.Fatalf("repeated snapshot state = %q, events = %d; want unknown, 1", state, events)
			}

			secondFailureAt := firstFailureAt.Add(authRecheckDelay)
			request.Executors["claude"] = protocol.ExecutorAuthReport{
				State: protocol.AuthExpired, Detail: "status check exited with code 1", CheckedAt: &secondFailureAt,
			}
			if err := store.RecordExecutorAuth(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if err := store.db.QueryRowContext(t.Context(), `SELECT state FROM worker_executor_auth WHERE worker_instance='worker-1' AND executor='claude'`).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != protocol.AuthExpired {
				t.Fatalf("second failed check state = %q, want expired", state)
			}
		})
	})
}
