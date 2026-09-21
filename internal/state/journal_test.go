package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedJournalSequence(t *testing.T, db *DB) *Sequence {
	t.Helper()

	start := time.Date(2026, 3, 1, 22, 0, 0, 0, time.UTC)
	done := start.Add(4 * time.Minute)

	seq := &Sequence{
		ID:          "seq-abc",
		PlanName:    "mains-outage",
		State:       "completed",
		StartedAt:   start,
		CompletedAt: &done,
		PonrCrossed: true,
		ResolvedAddrs: map[string]ResolvedAddr{
			"nas": {IP: "10.0.0.5", MAC: "aa:bb:cc:dd:ee:ff", ResolvedAt: start.Format(time.RFC3339)},
		},
		PreSequenceState: map[string]string{"nas": "up"},
	}
	if err := db.SaveSequence(t.Context(), seq); err != nil {
		t.Fatalf("SaveSequence: %v", err)
	}

	stageDone := start.Add(3 * time.Minute)
	if err := db.SaveStageRecord(t.Context(), &StageRecord{
		SequenceID: seq.ID, StageIndex: 0, StageName: "workstations",
		StartedAt: start, CompletedAt: &stageDone,
		Clients: map[string]ClientResult{"nas": {State: "down", StartedAt: start.Format(time.RFC3339)}},
	}); err != nil {
		t.Fatalf("SaveStageRecord: %v", err)
	}

	if err := db.SaveIntent(t.Context(), &Intent{
		ID: "int-1", SequenceID: seq.ID, ClientName: "nas", Action: "shutdown",
		Timestamp: start.Add(time.Second), Status: IntentDispatched,
		Result: &ActionResult{Success: true, Message: "ok"},
	}); err != nil {
		t.Fatalf("SaveIntent: %v", err)
	}

	return seq
}

func readJournal(t *testing.T, path string) []journalRecord {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading journal: %v", err)
	}

	var records []journalRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec journalRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line is not valid JSON (%q): %v", line, err)
		}
		records = append(records, rec)
	}
	return records
}

// TestExportJournalWritesOneLinePerEvent is SPEC §8.6: a JSONL file per
// sequence, derived from the database, holding the state transitions and
// command records rather than every polling cycle.
func TestExportJournalWritesOneLinePerEvent(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	seq := seedJournalSequence(t, db)

	path, err := db.ExportJournal(t.Context(), seq.ID)
	if err != nil {
		t.Fatalf("ExportJournal: %v", err)
	}

	if want := filepath.Join(dir, "journal", "seq-abc.jsonl"); path != want {
		t.Errorf("journal path = %q, want %q", path, want)
	}

	records := readJournal(t, path)
	if len(records) != 3 {
		t.Fatalf("got %d records, want a sequence header, a stage and an intent", len(records))
	}

	if records[0].Type != "sequence" {
		t.Errorf("first record is %q, want the sequence header", records[0].Type)
	}

	var header map[string]any
	if err := json.Unmarshal(records[0].Data, &header); err != nil {
		t.Fatalf("decoding header: %v", err)
	}
	if header["plan"] != "mains-outage" {
		t.Errorf("header plan = %v, want mains-outage", header["plan"])
	}
	if header["ponr_crossed"] != true {
		t.Error("the header does not record that the point of no return was crossed")
	}
	// The wake snapshot is the part that cannot be reconstructed later:
	// after the sequence the machines are off and their addresses cannot
	// be looked up again.
	addrs, ok := header["resolved_addrs"].(map[string]any)
	if !ok {
		t.Fatalf("header has no resolved_addrs: %v", header["resolved_addrs"])
	}
	nas, ok := addrs["nas"].(map[string]any)
	if !ok {
		t.Fatalf("resolved_addrs has no entry for nas: %v", addrs)
	}
	if nas["mac"] != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("recorded MAC = %v, want the pre-shutdown value", nas["mac"])
	}

	types := []string{records[0].Type, records[1].Type, records[2].Type}
	want := []string{"sequence", "stage", "intent"}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("record %d is %q, want %q (records are ordered by time)", i, types[i], want[i])
		}
	}
}

func TestExportJournalIsPrivate(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permissions are not meaningful")
	}

	dir := t.TempDir()
	db, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	seq := seedJournalSequence(t, db)
	path, err := db.ExportJournal(t.Context(), seq.ID)
	if err != nil {
		t.Fatalf("ExportJournal: %v", err)
	}

	// A journal names every client, its address and its MAC.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("journal mode = %o, want no group or other access", perm)
	}

	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("journal dir mode = %o, want no group or other access", perm)
	}
}

// TestExportJournalLeavesNoPartialFiles: the export is atomic, so a reader
// never sees a half-written journal that looks like a sequence which
// stopped early.
func TestExportJournalLeavesNoPartialFiles(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	seq := seedJournalSequence(t, db)
	if _, err := db.ExportJournal(t.Context(), seq.ID); err != nil {
		t.Fatalf("ExportJournal: %v", err)
	}

	entries, err := os.ReadDir(db.JournalDir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".journal-") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

func TestExportJournalRejectsUnknownSequence(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.ExportJournal(t.Context(), "nope"); err == nil {
		t.Error("exporting a sequence that does not exist succeeded")
	}
}

func TestJournalFileNameIsPathSafe(t *testing.T) {
	tests := map[string]string{
		"seq-abc":       "seq-abc.jsonl",
		"../../etc/shy": "______etc_shy.jsonl",
		"a/b":           "a_b.jsonl",
		"":              "unnamed.jsonl",
	}
	for in, want := range tests {
		if got := journalFileName(in); got != want {
			t.Errorf("journalFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPruneJournalFilesRespectsTheWindow(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	jdir := db.JournalDir()
	if err := os.MkdirAll(jdir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	old := filepath.Join(jdir, "old.jsonl")
	recent := filepath.Join(jdir, "recent.jsonl")
	other := filepath.Join(jdir, "notes.txt")
	for _, p := range []string{old, recent, other} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	long := time.Now().Add(-90 * 24 * time.Hour)
	if err := os.Chtimes(old, long, long); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	// An unrelated file in the directory must be left alone even when old.
	if err := os.Chtimes(other, long, long); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	removed, err := db.PruneJournalFiles(time.Now().Add(-30 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("PruneJournalFiles: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed %d files, want 1", removed)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the expired journal is still there")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Error("a journal inside the retention window was removed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("a non-journal file in the directory was removed")
	}
}

func TestPruneJournalFilesToleratesAMissingDirectory(t *testing.T) {
	db := newTestDB(t)
	removed, err := db.PruneJournalFiles(time.Now())
	if err != nil {
		t.Errorf("pruning before any sequence has run failed: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed %d files from a directory that does not exist", removed)
	}
}
