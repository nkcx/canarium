package state

import (
	"context"
	"fmt"
	"time"
)

// CreateSession records a session keyed by the SHA-256 digest of its token.
//
// The raw token is never stored: a database leak yields digests, which
// cannot be replayed as cookies.
func (d *DB) CreateSession(ctx context.Context, tokenHash string, ttl time.Duration) error {
	now := time.Now()
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO sessions (token_hash, created_at, expires_at)
		VALUES (?, ?, ?)
		ON CONFLICT(token_hash) DO UPDATE SET
			created_at=excluded.created_at,
			expires_at=excluded.expires_at
	`, tokenHash, now.Unix(), now.Add(ttl).Unix())
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	return nil
}

// SessionIsValid reports whether a session exists and has not expired.
//
// Expiry is evaluated in SQL so a session cannot be accepted because of a
// parse failure, which the previous kv-backed implementation risked.
func (d *DB) SessionIsValid(ctx context.Context, tokenHash string) (bool, error) {
	var count int
	err := d.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sessions WHERE token_hash = ? AND expires_at > ?",
		tokenHash, time.Now().Unix(),
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("validating session: %w", err)
	}
	return count > 0, nil
}

// DeleteSession removes a single session. Deleting one that does not exist is
// not an error, so logout is idempotent.
func (d *DB) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := d.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash); err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions prunes sessions past their expiry and reports how
// many rows were removed.
func (d *DB) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	res, err := d.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ?", time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("pruning expired sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("counting pruned sessions: %w", err)
	}
	return n, nil
}

// DeleteAllSessions invalidates every session. Used when the admin password
// changes, so a stolen cookie does not outlive the credential it was issued
// against.
func (d *DB) DeleteAllSessions(ctx context.Context) (int64, error) {
	res, err := d.db.ExecContext(ctx, "DELETE FROM sessions")
	if err != nil {
		return 0, fmt.Errorf("deleting all sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("counting deleted sessions: %w", err)
	}
	return n, nil
}

// CountSessions returns the number of sessions currently stored, expired
// included. Used by tests and diagnostics.
func (d *DB) CountSessions(ctx context.Context) (int, error) {
	var n int
	if err := d.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions").Scan(&n); err != nil {
		return 0, fmt.Errorf("counting sessions: %w", err)
	}
	return n, nil
}
