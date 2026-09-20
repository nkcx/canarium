package state

import (
	"testing"
	"time"
)

// seedSequence writes a sequence plus one intent and one stage record.
func seedSequence(t *testing.T, db *DB, id, state string, startedAt time.Time) {
	t.Helper()

	seq := &Sequence{
		ID:               id,
		PlanName:         "outage",
		State:            state,
		StartedAt:        startedAt,
		PreSequenceState: map[string]string{"nas": "up"},
	}
	if state != "shutting_down" && state != "waking" && state != "wake_gate" {
		completed := startedAt.Add(time.Minute)
		seq.CompletedAt = &completed
	}
	if err := db.SaveSequence(t.Context(), seq); err != nil {
		t.Fatalf("SaveSequence(%s): %v", id, err)
	}

	if err := db.SaveIntent(t.Context(), &Intent{
		ID:         id + "-intent",
		SequenceID: id,
		ClientName: "nas",
		Action:     "shutdown",
		Timestamp:  startedAt,
		Status:     "dispatched",
	}); err != nil {
		t.Fatalf("SaveIntent(%s): %v", id, err)
	}

	completed := startedAt.Add(time.Minute)
	if err := db.SaveStageRecord(t.Context(), &StageRecord{
		SequenceID:  id,
		StageIndex:  0,
		StageName:   "compute",
		StartedAt:   startedAt,
		CompletedAt: &completed,
		Clients:     map[string]ClientResult{"nas": {State: "down"}},
	}); err != nil {
		t.Fatalf("SaveStageRecord(%s): %v", id, err)
	}
}

// TestPruneJournalRemovesOldFinishedSequences is the regression test for
// journal_retain never being read: every record accumulated permanently.
func TestPruneJournalRemovesOldFinishedSequences(t *testing.T) {
	db := newTestDB(t)

	old := time.Now().Add(-60 * 24 * time.Hour)
	recent := time.Now().Add(-1 * time.Hour)

	seedSequence(t, db, "old-1", "completed", old)
	seedSequence(t, db, "old-2", "failed", old)
	seedSequence(t, db, "old-3", "aborted", old)
	seedSequence(t, db, "recent", "completed", recent)

	result, err := db.PruneJournal(t.Context(), time.Now().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatalf("PruneJournal: %v", err)
	}

	if result.Sequences != 3 {
		t.Errorf("pruned %d sequences, want 3", result.Sequences)
	}
	if result.Intents != 3 {
		t.Errorf("pruned %d intents, want 3", result.Intents)
	}
	if result.StageRecords != 3 {
		t.Errorf("pruned %d stage records, want 3", result.StageRecords)
	}

	counts, err := db.CountJournalRows(t.Context())
	if err != nil {
		t.Fatalf("CountJournalRows: %v", err)
	}
	if counts.Sequences != 1 {
		t.Errorf("%d sequences remain, want 1 (the recent one)", counts.Sequences)
	}
	if counts.Intents != 1 || counts.StageRecords != 1 {
		t.Errorf("child rows not pruned in step with their sequences: %+v", counts)
	}
}

// TestPruneJournalKeepsInProgressSequences: a hold or a wake gate waiting on
// a slowly recharging battery can legitimately outlive the window, and
// deleting it would strand the executor's resume state.
func TestPruneJournalKeepsInProgressSequences(t *testing.T) {
	db := newTestDB(t)

	old := time.Now().Add(-90 * 24 * time.Hour)
	for _, state := range []string{"shutting_down", "wake_gate", "waking"} {
		seedSequence(t, db, "inflight-"+state, state, old)
	}

	result, err := db.PruneJournal(t.Context(), time.Now().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatalf("PruneJournal: %v", err)
	}
	if result.Total() != 0 {
		t.Errorf("pruned %d rows belonging to in-progress sequences", result.Total())
	}

	counts, err := db.CountJournalRows(t.Context())
	if err != nil {
		t.Fatalf("CountJournalRows: %v", err)
	}
	if counts.Sequences != 3 {
		t.Errorf("%d in-progress sequences remain, want 3", counts.Sequences)
	}
}

func TestPruneJournalOnEmptyDatabase(t *testing.T) {
	db := newTestDB(t)

	result, err := db.PruneJournal(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("PruneJournal on an empty database: %v", err)
	}
	if result.Total() != 0 {
		t.Errorf("pruned %d rows from an empty database", result.Total())
	}
}

func TestPruneJournalIsIdempotent(t *testing.T) {
	db := newTestDB(t)

	seedSequence(t, db, "old", "completed", time.Now().Add(-60*24*time.Hour))
	cutoff := time.Now().Add(-30 * 24 * time.Hour)

	first, err := db.PruneJournal(t.Context(), cutoff)
	if err != nil {
		t.Fatalf("first PruneJournal: %v", err)
	}
	if first.Sequences != 1 {
		t.Fatalf("first pass pruned %d sequences, want 1", first.Sequences)
	}

	second, err := db.PruneJournal(t.Context(), cutoff)
	if err != nil {
		t.Fatalf("second PruneJournal: %v", err)
	}
	if second.Total() != 0 {
		t.Errorf("second pass pruned %d rows, want 0", second.Total())
	}
}

func TestVacuum(t *testing.T) {
	db := newTestDB(t)

	seedSequence(t, db, "old", "completed", time.Now().Add(-60*24*time.Hour))
	if _, err := db.PruneJournal(t.Context(), time.Now()); err != nil {
		t.Fatalf("PruneJournal: %v", err)
	}
	if err := db.Vacuum(t.Context()); err != nil {
		t.Errorf("Vacuum: %v", err)
	}
}
