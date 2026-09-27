package controlplane

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

func TestIncarnationInterrupts(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: "interval/build", Family: "interval", ConfigSignature: "v1"}}); err != nil {
		t.Fatal(err)
	}
	firstJob, created, err := store.CreateTriggeredJob(t.Context(), TriggerAdmission{
		Identity: "interval/build", Family: "interval", ConfigSignature: "v1", ConfigGeneration: mustTriggerGeneration(t, store, "interval/build"),
		ScheduledAt: time.Now().UTC(), Prompt: "first", Repository: "machinist", SelectionName: "plan", Command: testAgent("plan", "first"),
	})
	if err != nil || !created {
		t.Fatalf("create triggered job = %q, %v, %v", firstJob, created, err)
	}
	if _, err := store.CreateJob(t.Context(), "second", "machinist", "plan", testAgent("plan", "second")); err != nil {
		t.Fatal(err)
	}
	request := protocol.PollRequest{Incarnation: 1, InstanceID: "worker-stable", Name: "builder", Executors: []string{"codex"}, Repositories: []string{"machinist"}}
	first, err := store.Poll(t.Context(), request)
	if err != nil || first == nil {
		t.Fatalf("first lease = %#v, %v", first, err)
	}
	if first.Incarnation == 0 {
		t.Fatal("first lease has unknown incarnation")
	}

	request.Incarnation = first.Incarnation + 1
	second, err := store.Poll(t.Context(), request)
	if err != nil || second == nil {
		t.Fatalf("restart lease = %#v, %v", second, err)
	}
	if second.Incarnation <= first.Incarnation {
		t.Fatalf("restart incarnation = %d, want greater than %d", second.Incarnation, first.Incarnation)
	}
	var runState, jobState string
	var leaseExpires sql.NullInt64
	if err := store.db.QueryRowContext(t.Context(), `SELECT r.state,j.state,r.lease_expires_at FROM runs r JOIN jobs j ON j.id=r.job_id WHERE r.id=?`, first.ID).Scan(&runState, &jobState, &leaseExpires); err != nil {
		t.Fatal(err)
	}
	if runState != "interrupted" || jobState != "failed" || leaseExpires.Valid {
		t.Fatalf("interrupted run state = %q, job state = %q, lease expiry = %#v", runState, jobState, leaseExpires)
	}
	statuses, err := store.TriggerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].Health != "failed" || statuses[0].LatestError != "Worker process restarted" {
		t.Fatalf("trigger status = %#v", statuses)
	}

	stale := request
	stale.Incarnation = first.Incarnation
	staleRun, err := store.Poll(t.Context(), stale)
	if err != nil || staleRun != nil {
		t.Fatalf("stale process poll = %#v, %v", staleRun, err)
	}
	if err := store.db.QueryRowContext(t.Context(), `SELECT state FROM runs WHERE id=?`, second.ID).Scan(&runState); err != nil {
		t.Fatal(err)
	}
	if runState != "running" {
		t.Fatalf("new run state after stale poll = %q, want running", runState)
	}
	if err := store.Complete(t.Context(), second.ID, protocol.Completion{InstanceID: request.InstanceID, LeaseToken: second.LeaseToken, State: "succeeded"}); err != nil {
		t.Fatal(err)
	}

	workflowJob, err := store.createLegacyWorkflowJob(t.Context(), "workflow", "machinist", "deliver", []config.WorkflowStep{{Command: testAgent("plan", "workflow")}})
	if err != nil {
		t.Fatal(err)
	}
	request.Workflows = true
	workflowRun, err := store.Poll(t.Context(), request)
	if err != nil || workflowRun == nil {
		t.Fatalf("workflow lease = %#v, %v", workflowRun, err)
	}
	request.Incarnation++
	if run, err := store.Poll(t.Context(), request); err != nil || run != nil {
		t.Fatalf("workflow restart poll = %#v, %v", run, err)
	}
	if err := store.db.QueryRowContext(t.Context(), `SELECT state FROM jobs WHERE id=?`, workflowJob).Scan(&jobState); err != nil {
		t.Fatal(err)
	}
	if jobState != "interrupted" {
		t.Fatalf("workflow job state = %q, want interrupted", jobState)
	}
}
