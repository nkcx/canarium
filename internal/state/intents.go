package state

import (
	"context"
	"fmt"
	"time"
)

// Intent statuses.
const (
	// IntentDispatching means the executor recorded its intention to act
	// but the transport has not yet returned. An intent left in this state
	// after a restart means the outcome is unknown.
	IntentDispatching = "dispatching"

	// IntentDispatched means the transport returned.
	IntentDispatched = "dispatched"

	// IntentReconciled means the outcome was determined after a restart by
	// probing the client.
	IntentReconciled = "reconciled"
)

// PendingIntents returns intents for a sequence whose outcome is unknown.
//
// The intents table has been written since the first commit and, until now,
// never read. SPEC §8.4 specifies exactly this recovery step: an intent still
// marked "dispatching" means the daemon died between recording its intention
// and the transport returning, so the command may or may not have reached the
// host. The executor probes to find out rather than assuming either way.
func (d *DB) PendingIntents(ctx context.Context, sequenceID string) ([]*Intent, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, sequence_id, client_name, action, timestamp, status
		FROM intents
		WHERE sequence_id = ? AND status = ?
		ORDER BY timestamp
	`, sequenceID, IntentDispatching)
	if err != nil {
		return nil, fmt.Errorf("reading pending intents: %w", err)
	}
	defer rows.Close()

	var intents []*Intent
	for rows.Next() {
		var (
			intent    Intent
			timestamp string
		)
		if err := rows.Scan(&intent.ID, &intent.SequenceID, &intent.ClientName,
			&intent.Action, &timestamp, &intent.Status); err != nil {
			return nil, fmt.Errorf("scanning intent: %w", err)
		}

		intent.Timestamp, err = time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return nil, fmt.Errorf("parsing intent timestamp %q: %w", timestamp, err)
		}

		intents = append(intents, &intent)
	}

	return intents, rows.Err()
}

// SequenceIntents returns every intent recorded for a sequence, for the
// journal and for diagnostics.
func (d *DB) SequenceIntents(ctx context.Context, sequenceID string) ([]*Intent, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, sequence_id, client_name, action, timestamp, status
		FROM intents
		WHERE sequence_id = ?
		ORDER BY timestamp
	`, sequenceID)
	if err != nil {
		return nil, fmt.Errorf("reading intents: %w", err)
	}
	defer rows.Close()

	var intents []*Intent
	for rows.Next() {
		var (
			intent    Intent
			timestamp string
		)
		if err := rows.Scan(&intent.ID, &intent.SequenceID, &intent.ClientName,
			&intent.Action, &timestamp, &intent.Status); err != nil {
			return nil, fmt.Errorf("scanning intent: %w", err)
		}

		intent.Timestamp, err = time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return nil, fmt.Errorf("parsing intent timestamp %q: %w", timestamp, err)
		}

		intents = append(intents, &intent)
	}

	return intents, rows.Err()
}

// FailOrphanedSequence marks a sequence failed and releases its client locks.
//
// Used when a sequence cannot be resumed — typically because its plan is no
// longer in the configuration. Leaving it in place meant its locks were held
// forever and, because lock acquisition is re-entrant only for the holding
// sequence, no future sequence could ever act on those clients again without
// editing the database by hand.
func (d *DB) FailOrphanedSequence(ctx context.Context, sequenceID, reason string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning orphan cleanup: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	if _, err := tx.ExecContext(ctx, `
		UPDATE sequences SET state = 'failed', completed_at = ? WHERE id = ?
	`, time.Now().Format(time.RFC3339Nano), sequenceID); err != nil {
		return fmt.Errorf("marking sequence failed: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		"DELETE FROM client_locks WHERE sequence_id = ?", sequenceID); err != nil {
		return fmt.Errorf("releasing client locks: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE intents SET status = ?, result = ?
		WHERE sequence_id = ? AND status = ?
	`, IntentReconciled, `{"success":false,"message":`+quoteJSON(reason)+`}`,
		sequenceID, IntentDispatching); err != nil {
		return fmt.Errorf("closing pending intents: %w", err)
	}

	return tx.Commit()
}

// quoteJSON renders a string as a JSON string literal.
func quoteJSON(s string) string {
	var b []byte
	b = append(b, '"')
	for _, r := range s {
		switch r {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\n':
			b = append(b, '\\', 'n')
		default:
			b = append(b, string(r)...)
		}
	}
	return string(append(b, '"'))
}
