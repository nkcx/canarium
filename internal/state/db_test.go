package state

import (
	"os"
	"testing"
	"time"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenAndMigrate(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Fatal("db is nil")
	}
}

func TestClientStateRoundtrip(t *testing.T) {
	db := testDB(t)

	err := db.SaveClientState("alpha", "up", nil)
	if err != nil {
		t.Fatalf("saving client state: %v", err)
	}

	state, err := db.GetClientState("alpha")
	if err != nil {
		t.Fatalf("getting client state: %v", err)
	}
	if state != "up" {
		t.Errorf("state = %s, want up", state)
	}
}

func TestClientStateUpdate(t *testing.T) {
	db := testDB(t)

	db.SaveClientState("beta", "up", nil)
	db.SaveClientState("beta", "shutting_down", nil)

	state, _ := db.GetClientState("beta")
	if state != "shutting_down" {
		t.Errorf("state = %s, want shutting_down", state)
	}
}

func TestClientStateUnknownDefault(t *testing.T) {
	db := testDB(t)

	state, err := db.GetClientState("nonexistent")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if state != "unknown" {
		t.Errorf("state = %s, want unknown", state)
	}
}

func TestGetAllClientStates(t *testing.T) {
	db := testDB(t)

	db.SaveClientState("a", "up", nil)
	db.SaveClientState("b", "down", nil)

	states, err := db.GetAllClientStates()
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(states) != 2 {
		t.Errorf("count = %d, want 2", len(states))
	}
	if states["a"] != "up" {
		t.Errorf("a = %s, want up", states["a"])
	}
}

func TestSequenceRoundtrip(t *testing.T) {
	db := testDB(t)

	seq := &Sequence{
		ID:               "seq_1",
		PlanName:         "outage",
		State:            "shutting_down",
		CurrentStage:     1,
		PonrCrossed:      true,
		StartedAt:        time.Now(),
		PreSequenceState: map[string]string{"alpha": "up"},
	}

	err := db.SaveSequence(seq)
	if err != nil {
		t.Fatalf("saving sequence: %v", err)
	}

	loaded, err := db.GetActiveSequence()
	if err != nil {
		t.Fatalf("getting active sequence: %v", err)
	}
	if loaded == nil {
		t.Fatal("no active sequence found")
	}
	if loaded.PlanName != "outage" {
		t.Errorf("plan = %s, want outage", loaded.PlanName)
	}
	if loaded.CurrentStage != 1 {
		t.Errorf("stage = %d, want 1", loaded.CurrentStage)
	}
	if !loaded.PonrCrossed {
		t.Error("ponr_crossed = false, want true")
	}
}

func TestNoActiveSequence(t *testing.T) {
	db := testDB(t)

	seq, err := db.GetActiveSequence()
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if seq != nil {
		t.Error("expected no active sequence")
	}
}

func TestCompletedSequenceNotActive(t *testing.T) {
	db := testDB(t)

	now := time.Now()
	seq := &Sequence{
		ID:          "seq_done",
		PlanName:    "outage",
		State:       "completed",
		StartedAt:   now.Add(-10 * time.Minute),
		CompletedAt: &now,
	}
	db.SaveSequence(seq)

	active, _ := db.GetActiveSequence()
	if active != nil {
		t.Error("completed sequence should not be returned as active")
	}
}

func TestClientLocks(t *testing.T) {
	db := testDB(t)

	ok, err := db.AcquireClientLock("alpha", "seq_1")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !ok {
		t.Error("first acquire should succeed")
	}

	ok, _ = db.AcquireClientLock("alpha", "seq_2")
	if ok {
		t.Error("second acquire should fail (locked)")
	}

	db.ReleaseClientLock("alpha")
	ok, _ = db.AcquireClientLock("alpha", "seq_2")
	if !ok {
		t.Error("acquire after release should succeed")
	}
}

func TestReleaseSequenceLocks(t *testing.T) {
	db := testDB(t)

	db.AcquireClientLock("a", "seq_1")
	db.AcquireClientLock("b", "seq_1")
	db.AcquireClientLock("c", "seq_2")

	db.ReleaseSequenceLocks("seq_1")

	ok, _ := db.AcquireClientLock("a", "seq_3")
	if !ok {
		t.Error("a should be unlocked after sequence release")
	}
	ok, _ = db.AcquireClientLock("c", "seq_3")
	if ok {
		t.Error("c should still be locked (different sequence)")
	}
}

func TestIntentRoundtrip(t *testing.T) {
	db := testDB(t)

	seq := &Sequence{
		ID:       "seq_1",
		PlanName: "outage",
		State:    "shutting_down",
		StartedAt: time.Now(),
	}
	db.SaveSequence(seq)

	intent := &Intent{
		ID:         "int_1",
		SequenceID: "seq_1",
		ClientName: "alpha",
		Action:     "shutdown",
		Timestamp:  time.Now(),
		Status:     "dispatching",
	}

	err := db.SaveIntent(intent)
	if err != nil {
		t.Fatalf("saving intent: %v", err)
	}

	intent.Status = "dispatched"
	intent.Result = &ActionResult{Success: true, Message: "ok"}
	err = db.SaveIntent(intent)
	if err != nil {
		t.Fatalf("updating intent: %v", err)
	}
}

func TestKV(t *testing.T) {
	db := testDB(t)

	err := db.SetKV("mode", "armed")
	if err != nil {
		t.Fatalf("setting kv: %v", err)
	}

	val, err := db.GetKV("mode")
	if err != nil {
		t.Fatalf("getting kv: %v", err)
	}
	if val != "armed" {
		t.Errorf("kv = %s, want armed", val)
	}

	val, _ = db.GetKV("nonexistent")
	if val != "" {
		t.Errorf("nonexistent kv = %s, want empty", val)
	}
}

func TestPasswordHash(t *testing.T) {
	db := testDB(t)

	hash, _ := db.GetPasswordHash()
	if hash != "" {
		t.Error("initial hash should be empty")
	}

	db.SetPasswordHash("abc123hash")
	hash, _ = db.GetPasswordHash()
	if hash != "abc123hash" {
		t.Errorf("hash = %s, want abc123hash", hash)
	}
}

func TestAPIToken(t *testing.T) {
	db := testDB(t)

	db.SaveAPIToken("hash123", "test-token", "read")

	scope, err := db.ValidateAPIToken("hash123")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if scope != "read" {
		t.Errorf("scope = %s, want read", scope)
	}

	scope, _ = db.ValidateAPIToken("nonexistent")
	if scope != "" {
		t.Errorf("invalid token scope = %s, want empty", scope)
	}
}

func TestStageRecords(t *testing.T) {
	db := testDB(t)

	seq := &Sequence{ID: "seq_1", PlanName: "outage", State: "shutting_down", StartedAt: time.Now()}
	db.SaveSequence(seq)

	now := time.Now()
	rec := &StageRecord{
		SequenceID: "seq_1",
		StageIndex: 0,
		StageName:  "compute",
		StartedAt:  now,
		CompletedAt: &now,
	}
	db.SaveStageRecord(rec)

	completed, err := db.GetCompletedStages("seq_1")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(completed) != 1 || completed[0] != 0 {
		t.Errorf("completed = %v, want [0]", completed)
	}
}

func TestDBInTempDir(t *testing.T) {
	dir := t.TempDir()
	subdir := dir + "/sub/dir"

	db, err := Open(subdir)
	if err != nil {
		t.Fatalf("opening db in nested dir: %v", err)
	}
	db.Close()

	if _, err := os.Stat(subdir + "/state.db"); os.IsNotExist(err) {
		t.Error("state.db should exist")
	}
}
