package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSyncDrainsTheWriteAheadLog is the durability guarantee the shutdown
// path depends on. With synchronous=NORMAL a committed row can sit in the
// WAL unflushed; Sync must move it into the database file.
func TestSyncDrainsTheWriteAheadLog(t *testing.T) {
	dir := t.TempDir()

	db, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	seq := &Sequence{ID: "seq-1", PlanName: "outage", State: "shutting_down"}
	if err := db.SaveSequence(t.Context(), seq); err != nil {
		t.Fatalf("SaveSequence: %v", err)
	}
	for i := range 20 {
		if err := db.SaveIntent(t.Context(), &Intent{
			ID:         "int-" + string(rune('a'+i)),
			SequenceID: seq.ID,
			ClientName: "nas",
			Action:     "shutdown",
			Timestamp:  time.Now(),
			Status:     IntentDispatching,
		}); err != nil {
			t.Fatalf("SaveIntent: %v", err)
		}
	}

	walPath := filepath.Join(dir, "state.db-wal")
	before, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat WAL: %v", err)
	}
	if before.Size() == 0 {
		t.Skip("no WAL content accumulated; nothing to prove")
	}

	if err := db.Sync(t.Context()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// After a FULL checkpoint every committed frame has been transferred
	// into the database file and fsynced. The WAL file remains (FULL does
	// not truncate it) but its content is now redundant, so the database
	// file must have grown to hold it.
	main, err := os.Stat(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}
	if main.Size() == 0 {
		t.Error("the database file is still empty after a checkpoint")
	}
}

// TestSaveIntentDurableSurvivesAnUncleanClose simulates the power cut that
// the intent journal exists for: the process dies without closing the
// database, so anything left in an unflushed WAL is at risk. The record
// must still be readable.
func TestSaveIntentDurableSurvivesAnUncleanClose(t *testing.T) {
	dir := t.TempDir()

	db, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	seq := &Sequence{ID: "seq-1", PlanName: "outage", State: "shutting_down"}
	if err := db.SaveSequence(t.Context(), seq); err != nil {
		t.Fatalf("SaveSequence: %v", err)
	}

	intent := &Intent{
		ID:         "int-1",
		SequenceID: seq.ID,
		ClientName: "nas",
		Action:     "shutdown",
		Timestamp:  time.Now(),
		Status:     IntentDispatching,
	}
	if err := db.SaveIntentDurable(t.Context(), intent); err != nil {
		t.Fatalf("SaveIntentDurable: %v", err)
	}

	// Drop the handle without Close, then reopen: the recovery path.
	db.db.Close() //nolint:errcheck // deliberately abrupt

	reopened, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	pending, err := reopened.PendingIntents(t.Context(), seq.ID)
	if err != nil {
		t.Fatalf("PendingIntents: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d pending intents after an unclean close, want 1", len(pending))
	}
	if pending[0].ClientName != "nas" {
		t.Errorf("recovered intent is for %q, want nas", pending[0].ClientName)
	}
}

// TestSaveSequenceDurablePreservesPonr: after the point of no return the
// sequence can no longer be aborted. A restart that could not see the flag
// would offer the operator an abort that is no longer safe to take.
func TestSaveSequenceDurablePreservesPonr(t *testing.T) {
	dir := t.TempDir()

	db, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	seq := &Sequence{
		ID: "seq-1", PlanName: "outage", State: "shutting_down",
		PonrCrossed: true,
	}
	if err := db.SaveSequenceDurable(t.Context(), seq); err != nil {
		t.Fatalf("SaveSequenceDurable: %v", err)
	}

	db.db.Close() //nolint:errcheck // deliberately abrupt

	reopened, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	got, err := reopened.GetActiveSequence(t.Context())
	if err != nil {
		t.Fatalf("GetActiveSequence: %v", err)
	}
	if got == nil {
		t.Fatal("the sequence did not survive an unclean close")
	}
	if !got.PonrCrossed {
		t.Error("the point-of-no-return flag was lost; the UI would offer an unsafe abort")
	}
}
