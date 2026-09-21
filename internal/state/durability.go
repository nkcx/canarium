package state

import (
	"context"
	"fmt"
)

// Sync forces the write-ahead log to durable storage.
//
// The database runs in WAL mode with synchronous=NORMAL, which is the right
// default for a daemon on flash storage: commits are not fsynced
// individually, only at checkpoint boundaries, which keeps write
// amplification off an SD card during years of idle polling.
//
// The cost of that default is that a commit can be acknowledged and then
// lost if the machine dies before the next checkpoint. For most daemons
// that is a fair trade. For this one it is pointed at the wrong end: the
// event Canarium exists to handle is a power failure, so "the machine dies
// unexpectedly" is not a rare accident here, it is the main line. Losing
// the intent journal or the point-of-no-return flag in exactly the moment
// they are written is the worst possible time to lose them — on restart the
// executor would have no record that it had already told four machines to
// shut down, or that the sequence had passed the point where aborting is no
// longer safe.
//
// So the critical writes call Sync explicitly, per SPEC §8.4. Everything
// else — client state changes, stage records, dwell timers — rides the
// normal checkpoint schedule, because re-deriving those by probing is
// exactly what crash recovery already does.
//
// FULL rather than TRUNCATE: TRUNCATE additionally resets the WAL file,
// which blocks until every reader has finished. FULL transfers all
// committed frames into the database and fsyncs it, which is the durability
// property wanted here, without waiting on readers.
func (d *DB) Sync(ctx context.Context) error {
	// wal_checkpoint returns a row (busy, log frames, checkpointed frames).
	// database/sql requires it to be consumed, so this is a Query, not an
	// Exec, even though the result is not interesting.
	var busy, logFrames, checkpointed int
	err := d.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(FULL)`).
		Scan(&busy, &logFrames, &checkpointed)
	if err != nil {
		return fmt.Errorf("checkpointing write-ahead log: %w", err)
	}
	return nil
}

// SaveIntentDurable records an intent and forces it to durable storage
// before returning.
//
// Callers use this on the dispatch path, where the ordering guarantee is the
// whole point: the intent must be on disk before the command goes out, so a
// crash between the two is recoverable. An intent written but not flushed is
// indistinguishable, after a power cut, from an intent never written at all.
func (d *DB) SaveIntentDurable(ctx context.Context, intent *Intent) error {
	if err := d.SaveIntent(ctx, intent); err != nil {
		return err
	}
	return d.Sync(ctx)
}

// SaveSequenceDurable records a sequence and forces it to durable storage.
//
// Used when crossing the point of no return. After that flag is set the
// sequence can no longer be aborted, and a restart that failed to see it
// would offer the operator an abort that is no longer safe to take.
func (d *DB) SaveSequenceDurable(ctx context.Context, seq *Sequence) error {
	if err := d.SaveSequence(ctx, seq); err != nil {
		return err
	}
	return d.Sync(ctx)
}
