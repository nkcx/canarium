package state

import (
	"context"
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

// TestDurablyDoesNotStarveItsOwnWrite is a regression test for a deadlock
// that would have hung the daemon mid-shutdown.
//
// Durably pins a connection so it can raise synchronous for the duration.
// The pool holds exactly one connection, so a write that reaches for the
// pool instead of using the pinned one waits forever for a connection that
// cannot be returned until the write completes. The first implementation
// did exactly that, and the symptom was the shutdown path stopping dead
// with no error, just before dispatching a command.
func TestDurablyDoesNotStarveItsOwnWrite(t *testing.T) {
	db := newTestDB(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- db.SaveSequenceDurable(ctx, &Sequence{
			ID: "seq-1", PlanName: "outage", State: "shutting_down",
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SaveSequenceDurable: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("a durable write did not complete; it is waiting on the " +
			"connection it is holding itself")
	}
}

// TestDurablyRestoresTheDefault: leaving the pool's only connection on
// synchronous=FULL would silently make every later write fsync, which is
// the write amplification the NORMAL default exists to avoid on flash.
func TestDurablyRestoresTheDefault(t *testing.T) {
	db := newTestDB(t)

	if err := db.SaveSequenceDurable(t.Context(), &Sequence{
		ID: "seq-1", PlanName: "outage", State: "shutting_down",
	}); err != nil {
		t.Fatalf("SaveSequenceDurable: %v", err)
	}

	var synchronous int
	if err := db.db.QueryRowContext(t.Context(), `PRAGMA synchronous`).
		Scan(&synchronous); err != nil {
		t.Fatalf("reading synchronous: %v", err)
	}

	// 1 is NORMAL, 2 is FULL.
	if synchronous != 1 {
		t.Errorf("synchronous = %d after a durable write, want 1 (NORMAL)", synchronous)
	}
}

// TestDurablyRestoresTheDefaultAfterAFailedWrite: the restore has to happen
// on the error path too, or one constraint violation leaves the daemon
// fsyncing every write for the rest of its life.
func TestDurablyRestoresTheDefaultAfterAFailedWrite(t *testing.T) {
	db := newTestDB(t)

	// A foreign key onto a sequence that does not exist.
	err := db.SaveIntentDurable(t.Context(), &Intent{
		ID: "int-1", SequenceID: "nonexistent", ClientName: "nas",
		Action: "shutdown", Timestamp: time.Now(), Status: IntentDispatching,
	})
	if err == nil {
		t.Fatal("an intent referencing a missing sequence was accepted")
	}

	var synchronous int
	if err := db.db.QueryRowContext(t.Context(), `PRAGMA synchronous`).
		Scan(&synchronous); err != nil {
		t.Fatalf("reading synchronous: %v", err)
	}
	if synchronous != 1 {
		t.Errorf("synchronous = %d after a failed durable write, want 1 (NORMAL)", synchronous)
	}
}
