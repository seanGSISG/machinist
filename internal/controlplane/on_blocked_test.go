package controlplane

import (
	"path/filepath"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
)

func onBlockedWorkflow(t *testing.T, store *Store, maximum int) (string, *config.OnBlocked) {
	t.Helper()
	onBlocked := &config.OnBlocked{Goto: "plan", Max: maximum}
	steps := []config.WorkflowStep{
		{ID: "plan", Command: testAgent("plan", "issue URL")},
		{ID: "build", Command: testAgent("build", "issue URL"), OnBlocked: onBlocked},
	}
	id, err := store.createLegacyWorkflowJob(t.Context(), "issue URL", "machinist", "deliver", steps)
	if err != nil {
		t.Fatal(err)
	}
	return id, onBlocked
}

func TestOnBlockedGoto(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	id, _ := onBlockedWorkflow(t, store, 2)

	plan := pollWorkflowStep(t, store)
	finishStep(t, store, plan, "complete")
	finishStep(t, store, pollWorkflowStep(t, store), "blocked")

	assertJobState(t, store, id, "queued", 0)
	assertLatestAttempt(t, store.db, id, 0, 2, plan.ID, "loop")
	var count int
	if err := store.db.QueryRow(`SELECT count FROM workflow_loop_counters WHERE job_id=? AND edge='build->plan'`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("loop count = %d, want 1", count)
	}
}

func TestOnBlockedMax(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	id, _ := onBlockedWorkflow(t, store, 1)

	finishStep(t, store, pollWorkflowStep(t, store), "complete")
	finishStep(t, store, pollWorkflowStep(t, store), "blocked")
	finishStep(t, store, pollWorkflowStep(t, store), "complete")
	finishStep(t, store, pollWorkflowStep(t, store), "blocked")

	assertJobState(t, store, id, "blocked", 1)
	var count, runs int
	if err := store.db.QueryRow(`SELECT count FROM workflow_loop_counters WHERE job_id=? AND edge='build->plan'`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE job_id=?`, id).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if count != 1 || runs != 4 {
		t.Fatalf("loop count = %d, runs = %d; want 1, 4", count, runs)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].State != "blocked" {
		t.Fatalf("snapshot jobs = %#v", snapshot.Jobs)
	}
}
