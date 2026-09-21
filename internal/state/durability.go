package state

import (
	"context"
	"fmt"
)

// Durably runs a write and does not return until it has reached the disk.
//
// The database runs in WAL mode with synchronous=NORMAL, which is the right
// default for a daemon on flash storage: commits are not fsynced
// individually, only at checkpoint boundaries, which keeps write
// amplification off an SD card during years of idle polling.
//
// The cost of that default lands in the wrong place here. A commit can be
// acknowledged and then lost if the machine dies before the next
// checkpoint. For most daemons that is a fair trade. For this one it is
// pointed at exactly the wrong end: the event Canarium exists to handle is
// a power failure, so "the machine dies unexpectedly" is not a rare
// accident, it is the main line. Losing the intent journal in the moment it
// is written is the worst possible time to lose it -- on restart the
// executor would have no record that it had already told four machines to
// shut down, and would have to rediscover that by probing hosts that are no
// longer answering.
//
// So the critical writes wrap themselves in this, per SPEC §8.4. Everything
// else -- client state changes, stage records, dwell timers -- rides the
// normal checkpoint schedule, because re-deriving those by probing is
// exactly what crash recovery already does.
//
// Implementation: synchronous is raised to FULL for the duration, which
// fsyncs the write-ahead log on commit. The spec says "forces a WAL
// checkpoint", and a checkpoint does imply durability, but it gets there by
// transferring every pending frame into the database file and fsyncing
// that instead -- work proportional to the size of the log, repeated on
// every critical write, and it blocks on readers. Measured on this schema
// it is about 2.5x the cost for the same guarantee. Fsyncing the log is the
// primitive actually wanted.
//
// The connection is pinned for the duration because synchronous is
// per-connection state. The pool holds exactly one connection, so this
// serialises against every other database user until the write completes,
// which is the intent: nothing else should proceed on the belief that this
// record exists until it does.
func (d *DB) Durably(ctx context.Context, write func(context.Context, execer) error) error {
	conn, err := d.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquiring a connection for a durable write: %w", err)
	}
	defer conn.Close() //nolint:errcheck // returning it to the pool

	if _, err := conn.ExecContext(ctx, `PRAGMA synchronous = FULL`); err != nil {
		return fmt.Errorf("raising durability: %w", err)
	}

	// The write must run on this connection, not on the pool. The pool
	// holds one connection and it is this one, so reaching for another
	// would block forever waiting for a connection that cannot be returned
	// until the write it is waiting on completes.
	writeErr := write(ctx, conn)

	// Restore the default even if the write failed. Leaving the pool's only
	// connection on FULL would silently make every later write fsync, which
	// is the write amplification this whole arrangement exists to avoid.
	if _, err := conn.ExecContext(ctx, `PRAGMA synchronous = NORMAL`); err != nil && writeErr == nil {
		return fmt.Errorf("restoring durability: %w", err)
	}

	return writeErr
}

// Sync forces any already-committed writes to durable storage.
//
// Durably is the better tool when the write has not happened yet: it makes
// the commit itself durable rather than flushing after the fact. Sync
// exists for the case where a caller needs to know that everything written
// so far has landed -- notably before a deliberate shutdown.
func (d *DB) Sync(ctx context.Context) error {
	// wal_checkpoint returns a row, which database/sql requires be
	// consumed, so this is a Query rather than an Exec.
	var busy, logFrames, checkpointed int
	if err := d.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(FULL)`).
		Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("checkpointing write-ahead log: %w", err)
	}
	return nil
}

// SaveIntentDurable records an intent and does not return until it is on
// the disk.
//
// Callers use this on the dispatch path, where the ordering is the whole
// point: the intent must be durable before the command goes out, so a crash
// between the two is recoverable. An intent written but not flushed is
// indistinguishable, after a power cut, from an intent never written.
func (d *DB) SaveIntentDurable(ctx context.Context, intent *Intent) error {
	return d.Durably(ctx, func(ctx context.Context, ex execer) error {
		return saveIntentOn(ctx, ex, intent)
	})
}

// SaveSequenceDurable records a sequence and does not return until it is on
// the disk.
//
// Used when crossing the point of no return. After that flag is set the
// sequence can no longer be aborted, and a restart that failed to see it
// would offer the operator an abort that is no longer safe to take.
func (d *DB) SaveSequenceDurable(ctx context.Context, seq *Sequence) error {
	return d.Durably(ctx, func(ctx context.Context, ex execer) error {
		return saveSequenceOn(ctx, ex, seq)
	})
}
