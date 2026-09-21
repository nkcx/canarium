package state

import (
	"os"
	"path/filepath"
	"strings"
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

// TestExistingLooseDataDirIsReported: MkdirAll leaves an existing
// directory's mode alone, so a data directory created by a deployment script
// or a bind mount can be world-readable while holding the password hash,
// session tokens and learned SSH host keys.
func TestExistingLooseDataDirIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permissions are not meaningful")
	}

	dir := filepath.Join(t.TempDir(), "loose")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("creating dir: %v", err)
	}

	DataDirWarning = ""
	db, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if DataDirWarning == "" {
		t.Error("a world-readable data directory produced no warning")
	}
	if !strings.Contains(DataDirWarning, "chmod") {
		t.Errorf("the warning does not say how to fix it: %q", DataDirWarning)
	}
}

func TestTightDataDirProducesNoWarning(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tight")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("creating dir: %v", err)
	}

	DataDirWarning = ""
	db, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if DataDirWarning != "" {
		t.Errorf("a 0700 data directory produced a warning: %q", DataDirWarning)
	}
}

func TestCheckDataDirPermissions(t *testing.T) {
	tests := []struct {
		mode     os.FileMode
		wantWarn bool
	}{
		{0o700, false},
		{0o750, true},
		{0o755, true},
		{0o777, true},
		{0o600, false},
	}

	for _, tt := range tests {
		t.Run(tt.mode.String(), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "d")
			if err := os.Mkdir(dir, tt.mode); err != nil {
				t.Fatalf("creating dir: %v", err)
			}
			// Mkdir is subject to umask; set the mode explicitly.
			if err := os.Chmod(dir, tt.mode); err != nil {
				t.Fatalf("chmod: %v", err)
			}

			warning := checkDataDirPermissions(dir)
			if (warning != "") != tt.wantWarn {
				t.Errorf("mode %04o: warning=%q, wantWarn=%v", tt.mode, warning, tt.wantWarn)
			}
		})
	}
}
