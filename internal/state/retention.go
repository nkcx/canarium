package state

import (
	"context"
	"fmt"
	"time"
)

// PruneResult reports what a retention pass removed.
type PruneResult struct {
	Sequences    int64
	Intents      int64
	StageRecords int64
}

// Total returns the number of rows removed across all tables.
func (p PruneResult) Total() int64 {
	return p.Sequences + p.Intents + p.StageRecords
}

// PruneJournal deletes finished sequences that started before the cutoff,
// along with their intents and stage records.
//
// journal_retain has been a config field with a 30d default since the first
// commit, and nothing ever read it. Every sequence, every intent and every
// stage record accumulated permanently — on an SD card, in a daemon expected
// to run for years.
//
// Only terminal sequences are eligible. A sequence still in progress is
// never removed regardless of age: a long hold or a wake gate waiting on a
// slowly recharging battery can legitimately outlive the retention window,
// and deleting it would strand the executor's own resume state.
func (d *DB) PruneJournal(ctx context.Context, before time.Time) (PruneResult, error) {
	cutoff := before.Format(time.RFC3339Nano)

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return PruneResult{}, fmt.Errorf("beginning retention transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	// Children first: the schema declares foreign keys onto sequences, and
	// foreign_keys is now ON.
	const selectExpired = `
		SELECT id FROM sequences
		WHERE started_at < ?
		  AND state IN ('completed', 'failed', 'aborted')`

	var result PruneResult

	res, err := tx.ExecContext(ctx,
		`DELETE FROM intents WHERE sequence_id IN (`+selectExpired+`)`, cutoff)
	if err != nil {
		return PruneResult{}, fmt.Errorf("pruning intents: %w", err)
	}
	if result.Intents, err = res.RowsAffected(); err != nil {
		return PruneResult{}, fmt.Errorf("counting pruned intents: %w", err)
	}

	res, err = tx.ExecContext(ctx,
		`DELETE FROM stage_records WHERE sequence_id IN (`+selectExpired+`)`, cutoff)
	if err != nil {
		return PruneResult{}, fmt.Errorf("pruning stage records: %w", err)
	}
	if result.StageRecords, err = res.RowsAffected(); err != nil {
		return PruneResult{}, fmt.Errorf("counting pruned stage records: %w", err)
	}

	res, err = tx.ExecContext(ctx,
		`DELETE FROM sequences
		 WHERE started_at < ?
		   AND state IN ('completed', 'failed', 'aborted')`, cutoff)
	if err != nil {
		return PruneResult{}, fmt.Errorf("pruning sequences: %w", err)
	}
	if result.Sequences, err = res.RowsAffected(); err != nil {
		return PruneResult{}, fmt.Errorf("counting pruned sequences: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return PruneResult{}, fmt.Errorf("committing retention transaction: %w", err)
	}

	return result, nil
}

// CountJournalRows returns the current size of the journal tables, for tests
// and diagnostics.
func (d *DB) CountJournalRows(ctx context.Context) (PruneResult, error) {
	var out PruneResult

	for _, q := range []struct {
		table string
		dest  *int64
	}{
		{"sequences", &out.Sequences},
		{"intents", &out.Intents},
		{"stage_records", &out.StageRecords},
	} {
		if err := d.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+q.table).Scan(q.dest); err != nil {
			return PruneResult{}, fmt.Errorf("counting %s: %w", q.table, err)
		}
	}

	return out, nil
}

// Vacuum reclaims space left by deleted rows.
//
// SQLite does not return freed pages to the filesystem on its own, so
// without this the database file only ever grows even as retention removes
// rows from it.
func (d *DB) Vacuum(ctx context.Context) error {
	if _, err := d.db.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("vacuuming database: %w", err)
	}
	return nil
}
