package state

import "testing"

func TestAcquireClientLock(t *testing.T) {
	db := newTestDB(t)

	ok, err := db.AcquireClientLock("nas", "seq-1")
	if err != nil {
		t.Fatalf("AcquireClientLock: %v", err)
	}
	if !ok {
		t.Error("could not acquire an unheld lock")
	}
}

func TestClientLockExcludesOtherSequences(t *testing.T) {
	db := newTestDB(t)

	if ok, err := db.AcquireClientLock("nas", "seq-1"); err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}

	ok, err := db.AcquireClientLock("nas", "seq-2")
	if err != nil {
		t.Fatalf("AcquireClientLock: %v", err)
	}
	if ok {
		t.Error("a second sequence acquired a lock already held by another")
	}

	holder, err := db.ClientLockHolder("nas")
	if err != nil {
		t.Fatalf("ClientLockHolder: %v", err)
	}
	if holder != "seq-1" {
		t.Errorf("lock holder = %q, want seq-1", holder)
	}
}

// TestClientLockIsReentrantForSameSequence is the regression test for
// resumption after a crash. Lock rows survive the restart, so a sequence
// resuming must be able to reclaim its own locks — otherwise every client is
// skipped as "locked by another sequence" and the resumed sequence silently
// shuts nothing down.
func TestClientLockIsReentrantForSameSequence(t *testing.T) {
	db := newTestDB(t)

	if ok, err := db.AcquireClientLock("nas", "seq-1"); err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}

	ok, err := db.AcquireClientLock("nas", "seq-1")
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if !ok {
		t.Error("a sequence could not reclaim its own lock after a restart")
	}
}

func TestReleaseClientLock(t *testing.T) {
	db := newTestDB(t)

	if ok, err := db.AcquireClientLock("nas", "seq-1"); err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	if err := db.ReleaseClientLock("nas"); err != nil {
		t.Fatalf("ReleaseClientLock: %v", err)
	}

	ok, err := db.AcquireClientLock("nas", "seq-2")
	if err != nil {
		t.Fatalf("AcquireClientLock: %v", err)
	}
	if !ok {
		t.Error("a released lock was not reacquirable by another sequence")
	}
}

func TestReleaseSequenceLocksReleasesAll(t *testing.T) {
	db := newTestDB(t)

	for _, name := range []string{"nas", "hypervisor", "firewall"} {
		if ok, err := db.AcquireClientLock(name, "seq-1"); err != nil || !ok {
			t.Fatalf("acquire %s: ok=%v err=%v", name, ok, err)
		}
	}
	// A lock held by a different sequence must survive.
	if ok, err := db.AcquireClientLock("other", "seq-2"); err != nil || !ok {
		t.Fatalf("acquire other: ok=%v err=%v", ok, err)
	}

	if err := db.ReleaseSequenceLocks("seq-1"); err != nil {
		t.Fatalf("ReleaseSequenceLocks: %v", err)
	}

	for _, name := range []string{"nas", "hypervisor", "firewall"} {
		holder, err := db.ClientLockHolder(name)
		if err != nil {
			t.Fatalf("ClientLockHolder(%s): %v", name, err)
		}
		if holder != "" {
			t.Errorf("%s is still locked by %q after releasing the sequence", name, holder)
		}
	}

	holder, err := db.ClientLockHolder("other")
	if err != nil {
		t.Fatalf("ClientLockHolder(other): %v", err)
	}
	if holder != "seq-2" {
		t.Errorf("an unrelated sequence's lock was released: holder = %q, want seq-2", holder)
	}
}

func TestClientLockHolderForUnlockedClient(t *testing.T) {
	db := newTestDB(t)

	holder, err := db.ClientLockHolder("never-locked")
	if err != nil {
		t.Fatalf("ClientLockHolder: %v", err)
	}
	if holder != "" {
		t.Errorf("holder = %q for an unlocked client, want empty", holder)
	}
}
