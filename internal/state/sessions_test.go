package state

import (
	"testing"
	"time"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSessionRoundTrip(t *testing.T) {
	db := newTestDB(t)

	const hash = "deadbeef"
	if err := db.CreateSession(hash, time.Hour); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	valid, err := db.SessionIsValid(hash)
	if err != nil {
		t.Fatalf("SessionIsValid: %v", err)
	}
	if !valid {
		t.Error("a freshly created session is not valid")
	}
}

func TestUnknownSessionIsInvalid(t *testing.T) {
	db := newTestDB(t)

	valid, err := db.SessionIsValid("never-created")
	if err != nil {
		t.Fatalf("SessionIsValid: %v", err)
	}
	if valid {
		t.Error("an unknown session token was accepted")
	}
}

func TestExpiredSessionIsInvalid(t *testing.T) {
	db := newTestDB(t)

	const hash = "expired"
	if err := db.CreateSession(hash, -time.Minute); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	valid, err := db.SessionIsValid(hash)
	if err != nil {
		t.Fatalf("SessionIsValid: %v", err)
	}
	if valid {
		t.Error("an expired session was accepted")
	}
}

func TestDeleteSession(t *testing.T) {
	db := newTestDB(t)

	const hash = "to-delete"
	if err := db.CreateSession(hash, time.Hour); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := db.DeleteSession(hash); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	valid, err := db.SessionIsValid(hash)
	if err != nil {
		t.Fatalf("SessionIsValid: %v", err)
	}
	if valid {
		t.Error("a deleted session was still accepted")
	}

	// Deleting again must not error; logout is idempotent.
	if err := db.DeleteSession(hash); err != nil {
		t.Errorf("second DeleteSession: %v", err)
	}
}

// TestDeleteExpiredSessionsPrunesOnlyExpired is the regression test for
// unbounded session growth: nothing ever removed session rows before.
func TestDeleteExpiredSessionsPrunesOnlyExpired(t *testing.T) {
	db := newTestDB(t)

	for i, s := range []struct {
		hash string
		ttl  time.Duration
	}{
		{"live-1", time.Hour},
		{"live-2", time.Hour},
		{"dead-1", -time.Hour},
		{"dead-2", -time.Minute},
		{"dead-3", -time.Second},
	} {
		if err := db.CreateSession(s.hash, s.ttl); err != nil {
			t.Fatalf("CreateSession %d: %v", i, err)
		}
	}

	pruned, err := db.DeleteExpiredSessions()
	if err != nil {
		t.Fatalf("DeleteExpiredSessions: %v", err)
	}
	if pruned != 3 {
		t.Errorf("pruned %d sessions, want 3", pruned)
	}

	remaining, err := db.CountSessions()
	if err != nil {
		t.Fatalf("CountSessions: %v", err)
	}
	if remaining != 2 {
		t.Errorf("%d sessions remain, want 2", remaining)
	}

	for _, hash := range []string{"live-1", "live-2"} {
		valid, err := db.SessionIsValid(hash)
		if err != nil {
			t.Fatalf("SessionIsValid(%q): %v", hash, err)
		}
		if !valid {
			t.Errorf("pruning removed the live session %q", hash)
		}
	}
}

func TestDeleteAllSessions(t *testing.T) {
	db := newTestDB(t)

	for _, hash := range []string{"a", "b", "c"} {
		if err := db.CreateSession(hash, time.Hour); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
	}

	n, err := db.DeleteAllSessions()
	if err != nil {
		t.Fatalf("DeleteAllSessions: %v", err)
	}
	if n != 3 {
		t.Errorf("deleted %d sessions, want 3", n)
	}

	count, err := db.CountSessions()
	if err != nil {
		t.Fatalf("CountSessions: %v", err)
	}
	if count != 0 {
		t.Errorf("%d sessions remain after DeleteAllSessions", count)
	}
}

func TestCreateSessionIsIdempotentPerToken(t *testing.T) {
	db := newTestDB(t)

	const hash = "reused"
	if err := db.CreateSession(hash, time.Hour); err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}
	if err := db.CreateSession(hash, 2*time.Hour); err != nil {
		t.Fatalf("second CreateSession: %v", err)
	}

	count, err := db.CountSessions()
	if err != nil {
		t.Fatalf("CountSessions: %v", err)
	}
	if count != 1 {
		t.Errorf("%d rows for one token, want 1", count)
	}
}
