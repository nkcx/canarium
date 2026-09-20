package state

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDatabasePermissionsAreRestrictive guards the file holding the admin
// password hash, live session tokens and API token digests. The data
// directory was created 0755, making all of that world-readable.
func TestDatabasePermissionsAreRestrictive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "canarium")

	db, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Write something so the WAL sibling exists.
	if err := db.SetPasswordHash(t.Context(), "$2a$10$abcdefghijklmnopqrstuv"); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat data dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("data dir mode = %o, want no group or other access", perm)
	}

	for _, name := range []string{"state.db", "state.db-wal", "state.db-shm"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s mode = %o, want no group or other access", name, perm)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	first, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := first.SetPasswordHash(t.Context(), "hash"); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}
	first.Close()

	second, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer second.Close()

	hash, err := second.GetPasswordHash(t.Context())
	if err != nil {
		t.Fatalf("GetPasswordHash: %v", err)
	}
	if hash != "hash" {
		t.Errorf("password hash = %q after reopen, want it preserved", hash)
	}
}

// TestConcurrentWritesDoNotFail exercises the single-writer pool against the
// pattern a stage produces: several clients recording intents at once.
func TestConcurrentWritesDoNotFail(t *testing.T) {
	db := newTestDB(t)

	seq := &Sequence{ID: "seq-1", PlanName: "outage", State: "shutting_down"}
	if err := db.SaveSequence(t.Context(), seq); err != nil {
		t.Fatalf("SaveSequence: %v", err)
	}

	ctx := t.Context()

	const writers = 16
	errs := make(chan error, writers)

	for i := 0; i < writers; i++ {
		go func(i int) {
			errs <- db.SaveClientState(ctx,
				"client-"+string(rune('a'+i%26)), "shutting_down", &seq.ID)
		}(i)
	}

	for i := 0; i < writers; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent write %d failed: %v", i, err)
		}
	}
}
