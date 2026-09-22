package state

import (
	"context"
	"fmt"
	"time"
)

// LearnedMAC is a hardware address Canarium discovered rather than read
// from the configuration file.
type LearnedMAC struct {
	Client string
	MAC    string

	// Source says where it came from, for display: which API, or the
	// kernel's neighbour table.
	Source string

	// LearnedAt is when this address was first seen for this client, and
	// ConfirmedAt when it was last checked. They differ once an address has
	// been re-confirmed, which is how an operator tells a value checked
	// minutes ago from one last seen months back.
	LearnedAt   time.Time
	ConfirmedAt time.Time
}

// SaveLearnedMAC records a discovered address.
//
// An address that has not changed keeps its original learned_at and only
// moves confirmed_at forward. A different address replaces the row and
// restarts both, because it is a different piece of hardware.
func (d *DB) SaveLearnedMAC(ctx context.Context, m LearnedMAC) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO learned_macs (client_name, mac, source, learned_at, confirmed_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(client_name) DO UPDATE SET
			mac=excluded.mac,
			source=excluded.source,
			learned_at=CASE
				WHEN learned_macs.mac = excluded.mac THEN learned_macs.learned_at
				ELSE excluded.learned_at
			END,
			confirmed_at=excluded.confirmed_at
	`, m.Client, m.MAC, m.Source,
		m.LearnedAt.Format(time.RFC3339Nano), m.ConfirmedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("recording learned MAC for %s: %w", m.Client, err)
	}
	return nil
}

// LearnedMACs returns every recorded address, keyed by client name.
func (d *DB) LearnedMACs(ctx context.Context) (map[string]LearnedMAC, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT client_name, mac, source, learned_at, confirmed_at FROM learned_macs
	`)
	if err != nil {
		return nil, fmt.Errorf("reading learned MACs: %w", err)
	}
	defer rows.Close()

	out := make(map[string]LearnedMAC)
	for rows.Next() {
		var (
			m                    LearnedMAC
			learnedAt, confirmed string
		)
		if err := rows.Scan(&m.Client, &m.MAC, &m.Source, &learnedAt, &confirmed); err != nil {
			return nil, fmt.Errorf("scanning learned MAC: %w", err)
		}

		// A row with an unparseable timestamp is still a usable address;
		// losing the MAC over a formatting problem would be the worse
		// outcome, so the zero time stands in.
		m.LearnedAt, _ = time.Parse(time.RFC3339Nano, learnedAt)
		m.ConfirmedAt, _ = time.Parse(time.RFC3339Nano, confirmed)

		out[m.Client] = m
	}

	return out, rows.Err()
}

// ForgetLearnedMAC removes a client's recorded address.
func (d *DB) ForgetLearnedMAC(ctx context.Context, client string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM learned_macs WHERE client_name = ?`, client)
	return err
}
