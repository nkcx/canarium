package state

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// journalDirMode and journalFileMode keep exported journals as private as
// the database they are derived from. A journal names every client, its
// address and its MAC.
const (
	journalDirMode  = 0o700
	journalFileMode = 0o600
)

// JournalDir returns the directory holding exported sequence journals.
func (d *DB) JournalDir() string {
	return filepath.Join(d.dataDir, "journal")
}

// journalRecord is one line of a journal file.
//
// JSONL rather than a single JSON document so a journal can be read
// incrementally, tailed while a long sequence is still being written, and
// grepped with ordinary tools. Every line carries its own type tag so a
// reader never has to track position to know what it is looking at.
type journalRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

// ExportJournal writes a sequence's record to a JSONL file and returns its
// path.
//
// SPEC §8.6: one file per sequence, generated from the database rather than
// being the primary store, rotated on completion. The database is the
// authority; this is the artefact an operator can archive, diff, hand to
// someone investigating an outage, or feed to the replay tooling.
//
// The distinction matters for what belongs here. This records what the
// sequence *did* -- when it started, which stages ran, what was dispatched
// to each client and what came back -- not every condition evaluation from
// every polling cycle. A daemon that logged each poll would write
// continuously to flash for years to capture almost nothing.
//
// Writing is atomic: the file is built alongside and renamed into place, so
// a crash mid-export leaves either the previous journal or none, never a
// truncated one that a reader would mistake for a sequence that stopped
// early.
func (d *DB) ExportJournal(ctx context.Context, sequenceID string) (string, error) {
	seq, err := d.GetSequenceByID(ctx, sequenceID)
	if err != nil {
		return "", fmt.Errorf("reading sequence: %w", err)
	}
	if seq == nil {
		return "", fmt.Errorf("sequence %q not found", sequenceID)
	}

	stages, err := d.StageRecords(ctx, sequenceID)
	if err != nil {
		return "", fmt.Errorf("reading stage records: %w", err)
	}

	intents, err := d.SequenceIntents(ctx, sequenceID)
	if err != nil {
		return "", fmt.Errorf("reading intents: %w", err)
	}

	records, err := buildJournal(seq, stages, intents)
	if err != nil {
		return "", err
	}

	dir := d.JournalDir()
	if err := os.MkdirAll(dir, journalDirMode); err != nil {
		return "", fmt.Errorf("creating journal dir: %w", err)
	}

	path := filepath.Join(dir, journalFileName(sequenceID))

	tmp, err := os.CreateTemp(dir, ".journal-*")
	if err != nil {
		return "", fmt.Errorf("creating journal file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed

	if err := writeJournal(tmp, records); err != nil {
		tmp.Close() //nolint:errcheck // the write already failed
		return "", err
	}
	if err := tmp.Chmod(journalFileMode); err != nil {
		tmp.Close() //nolint:errcheck // reporting the chmod failure
		return "", fmt.Errorf("securing journal file: %w", err)
	}
	// Flush before the rename: a rename is atomic with respect to the
	// directory entry, but says nothing about the file's contents having
	// reached the disk.
	if err := tmp.Sync(); err != nil {
		tmp.Close() //nolint:errcheck // reporting the sync failure
		return "", fmt.Errorf("flushing journal file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("closing journal file: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("publishing journal file: %w", err)
	}

	return path, nil
}

func writeJournal(f *os.File, records []journalRecord) error {
	enc := json.NewEncoder(f)
	for _, rec := range records {
		if err := enc.Encode(rec); err != nil {
			return fmt.Errorf("writing journal record: %w", err)
		}
	}
	return nil
}

// buildJournal assembles the records for one sequence in time order.
func buildJournal(seq *Sequence, stages []*StageRecord, intents []*Intent) ([]journalRecord, error) {
	var records []journalRecord

	add := func(kind string, ts time.Time, payload any) error {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encoding %s record: %w", kind, err)
		}
		records = append(records, journalRecord{
			Type:      kind,
			Timestamp: ts.UTC().Format(time.RFC3339Nano),
			Data:      b,
		})
		return nil
	}

	// The header carries the wake snapshot -- the addresses and MACs as
	// they were resolved before anything was shut down. Recording them is
	// the point: after a sequence, the machines it describes are off, and
	// their addresses can no longer be looked up.
	header := map[string]any{
		"sequence_id":        seq.ID,
		"plan":               seq.PlanName,
		"final_state":        seq.State,
		"aborted":            seq.Aborted,
		"ponr_crossed":       seq.PonrCrossed,
		"stages_entered":     seq.CurrentStage,
		"started_at":         seq.StartedAt.UTC().Format(time.RFC3339Nano),
		"pre_sequence_state": seq.PreSequenceState,
		"resolved_addrs":     seq.ResolvedAddrs,
	}
	if seq.CompletedAt != nil {
		header["completed_at"] = seq.CompletedAt.UTC().Format(time.RFC3339Nano)
		header["duration"] = seq.CompletedAt.Sub(seq.StartedAt).String()
	}
	if err := add("sequence", seq.StartedAt, header); err != nil {
		return nil, err
	}

	for _, st := range stages {
		payload := map[string]any{
			"sequence_id": st.SequenceID,
			"index":       st.StageIndex,
			"name":        st.StageName,
			"started_at":  st.StartedAt.UTC().Format(time.RFC3339Nano),
			"clients":     st.Clients,
		}
		if st.CompletedAt != nil {
			payload["completed_at"] = st.CompletedAt.UTC().Format(time.RFC3339Nano)
			payload["duration"] = st.CompletedAt.Sub(st.StartedAt).String()
		} else {
			// A stage with no completion is how an abort or a crash shows
			// up. Saying so beats leaving a reader to infer it.
			payload["completed"] = false
		}
		if err := add("stage", st.StartedAt, payload); err != nil {
			return nil, err
		}
	}

	for _, in := range intents {
		payload := map[string]any{
			"sequence_id": in.SequenceID,
			"client":      in.ClientName,
			"action":      in.Action,
			"status":      in.Status,
		}
		if in.Result != nil {
			payload["success"] = in.Result.Success
			if in.Result.Message != "" {
				payload["message"] = in.Result.Message
			}
		}
		if err := add("intent", in.Timestamp, payload); err != nil {
			return nil, err
		}
	}

	// Stable ordering by timestamp, with the header first regardless: a
	// stage and an intent recorded in the same instant should still read
	// in a consistent order across exports of the same sequence.
	sort.SliceStable(records, func(i, j int) bool {
		if (records[i].Type == "sequence") != (records[j].Type == "sequence") {
			return records[i].Type == "sequence"
		}
		return records[i].Timestamp < records[j].Timestamp
	})

	return records, nil
}

// journalFileName maps a sequence ID onto a filename.
//
// Sequence IDs are generated internally and are already path-safe, but this
// file is named from data that reaches an os.Create, so it is sanitised
// rather than trusted. A malformed ID should produce an oddly named file,
// never a write outside the journal directory.
func journalFileName(sequenceID string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, sequenceID)

	if safe == "" {
		safe = "unnamed"
	}
	return safe + ".jsonl"
}

// PruneJournalFiles removes exported journals last modified before the
// cutoff, returning how many were deleted.
//
// Kept separate from PruneJournal, which prunes the database, because the
// two can legitimately disagree: an operator may keep files for an audit
// window longer than the daemon keeps rows, or copy them elsewhere and let
// these expire. Both run from the same retention setting by default.
func (d *DB) PruneJournalFiles(before time.Time) (int, error) {
	dir := d.JournalDir()

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading journal dir: %w", err)
	}

	var (
		removed  int
		firstErr error
	)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue // vanished under us; nothing to remove
		}
		if !info.ModTime().Before(before) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			// Keep going: one unremovable file should not strand the rest.
			if firstErr == nil {
				firstErr = fmt.Errorf("removing %s: %w", entry.Name(), err)
			}
			continue
		}
		removed++
	}

	return removed, firstErr
}
