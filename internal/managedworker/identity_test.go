package managedworker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstanceIDPersists(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "worker")
	first, err := LoadOrCreateInstanceID(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateInstanceID(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("instance IDs differ: %q != %q", first, second)
	}
	fresh, err := LoadOrCreateInstanceID(filepath.Join(t.TempDir(), "worker"))
	if err != nil {
		t.Fatal(err)
	}
	if fresh == first {
		t.Fatalf("fresh state directory reused instance ID %q", fresh)
	}
	info, err := os.Stat(filepath.Join(stateDir, "instance_id"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("instance ID mode = %o, want 600", info.Mode().Perm())
	}
	firstIncarnation, err := nextIncarnation(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	secondIncarnation, err := nextIncarnation(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if secondIncarnation <= firstIncarnation {
		t.Fatalf("second incarnation = %d, want greater than %d", secondIncarnation, firstIncarnation)
	}
}
