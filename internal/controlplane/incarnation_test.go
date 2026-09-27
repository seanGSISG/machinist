package controlplane

import (
	"path/filepath"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
)

func TestIncarnationInterrupts(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	for _, prompt := range []string{"first", "second"} {
		if _, err := store.CreateJob(t.Context(), prompt, "machinist", "plan", testAgent("plan", prompt)); err != nil {
			t.Fatal(err)
		}
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
	var state string
	if err := store.db.QueryRowContext(t.Context(), `SELECT state FROM runs WHERE id=?`, first.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "interrupted" {
		t.Fatalf("old run state = %q, want interrupted", state)
	}
}
