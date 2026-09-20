package state

import (
	"context"
	"fmt"
	"time"

	"github.com/nkcx/canarium/internal/conditions"
)

// LoadDwellTrackers returns every persisted dwell tracker, keyed by condition.
//
// Implements conditions.DwellStore.
func (d *DB) LoadDwellTrackers(ctx context.Context) (map[string]conditions.DwellRecord, error) {
	rows, err := d.db.QueryContext(ctx,
		"SELECT condition_key, required_ns, elapsed_ns, satisfied, last_seen_at FROM dwell_trackers")
	if err != nil {
		return nil, fmt.Errorf("loading dwell trackers: %w", err)
	}
	defer rows.Close()

	out := make(map[string]conditions.DwellRecord)
	for rows.Next() {
		var (
			key        string
			requiredNS int64
			elapsedNS  int64
			satisfied  bool
			lastSeen   int64
		)
		if err := rows.Scan(&key, &requiredNS, &elapsedNS, &satisfied, &lastSeen); err != nil {
			return nil, fmt.Errorf("scanning dwell tracker: %w", err)
		}
		out[key] = conditions.DwellRecord{
			RequiredNS: requiredNS,
			ElapsedNS:  elapsedNS,
			Satisfied:  satisfied,
			LastSeen:   time.Unix(lastSeen, 0),
		}
	}
	return out, rows.Err()
}

// SaveDwellTracker writes a tracker's progress.
func (d *DB) SaveDwellTracker(ctx context.Context, key string, rec conditions.DwellRecord) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO dwell_trackers (condition_key, required_ns, elapsed_ns, satisfied, last_seen_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(condition_key) DO UPDATE SET
			required_ns=excluded.required_ns,
			elapsed_ns=excluded.elapsed_ns,
			satisfied=excluded.satisfied,
			last_seen_at=excluded.last_seen_at
	`, key, rec.RequiredNS, rec.ElapsedNS, rec.Satisfied, rec.LastSeen.Unix())
	if err != nil {
		return fmt.Errorf("saving dwell tracker: %w", err)
	}
	return nil
}

// DeleteDwellTracker removes a tracker.
func (d *DB) DeleteDwellTracker(ctx context.Context, key string) error {
	if _, err := d.db.ExecContext(ctx, "DELETE FROM dwell_trackers WHERE condition_key = ?", key); err != nil {
		return fmt.Errorf("deleting dwell tracker: %w", err)
	}
	return nil
}

// PruneDwellTrackers removes trackers not seen since the given time. Used to
// clean up after conditions are removed from the config.
func (d *DB) PruneDwellTrackers(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.db.ExecContext(ctx, "DELETE FROM dwell_trackers WHERE last_seen_at < ?", before.Unix())
	if err != nil {
		return 0, fmt.Errorf("pruning dwell trackers: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("counting pruned dwell trackers: %w", err)
	}
	return n, nil
}
