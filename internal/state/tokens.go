package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// API token scopes.
const (
	// ScopeRead permits read-only endpoints.
	ScopeRead = "read"

	// ScopeAdmin permits everything, including changing mode and aborting a
	// sequence.
	ScopeAdmin = "admin"
)

// ValidScopes lists every accepted scope.
var ValidScopes = []string{ScopeRead, ScopeAdmin}

// APIToken describes a stored token. The token itself is never retained,
// only its digest, so it cannot be recovered after creation.
type APIToken struct {
	Name      string
	Scope     string
	CreatedAt time.Time
	LastUsed  *time.Time
}

// ErrTokenExists is returned when a token name is already taken.
var ErrTokenExists = errors.New("a token with that name already exists")

// SaveAPIToken stores a token digest under a unique name.
func (d *DB) SaveAPIToken(ctx context.Context, tokenHash, name, scope string) error {
	var existing int
	if err := d.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM api_tokens WHERE name = ?", name).Scan(&existing); err != nil {
		return fmt.Errorf("checking for an existing token: %w", err)
	}
	if existing > 0 {
		return ErrTokenExists
	}

	if _, err := d.db.ExecContext(ctx, `
		INSERT INTO api_tokens (token_hash, name, scope, created_at)
		VALUES (?, ?, ?, ?)
	`, tokenHash, name, scope, time.Now().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("saving API token: %w", err)
	}
	return nil
}

// lastUsedResolution is how coarsely token use is recorded.
//
// last_used_at answers "is this token still in use?", for which five-minute
// granularity is ample. Recording every use meant a SQLite write on every
// authenticated request: a monitoring system polling /api/status every few
// seconds produced a continuous write stream, and because the pool is capped
// at a single connection each write blocked concurrent readers. On the SD
// card this is expected to run from, that is also avoidable flash wear.
const lastUsedResolution = 5 * time.Minute

// ValidateAPIToken returns the scope for a token digest, or "" if unknown.
func (d *DB) ValidateAPIToken(ctx context.Context, tokenHash string) (string, error) {
	var (
		scope    string
		lastUsed sql.NullInt64
	)
	err := d.db.QueryRowContext(ctx, `
		SELECT scope, COALESCE(strftime('%s', last_used_at), 0)
		FROM api_tokens WHERE token_hash = ?
	`, tokenHash).Scan(&scope, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("validating API token: %w", err)
	}

	d.recordTokenUse(ctx, tokenHash, time.Unix(lastUsed.Int64, 0))
	return scope, nil
}

// recordTokenUse updates last_used_at, but only when it is stale enough to
// be worth a write.
//
// Failures are ignored on purpose: this is diagnostic bookkeeping, and
// failing authentication because a full disk rejected it would turn a
// housekeeping problem into an outage.
func (d *DB) recordTokenUse(ctx context.Context, tokenHash string, lastUsed time.Time) {
	now := time.Now()
	if !lastUsed.IsZero() && now.Sub(lastUsed) < lastUsedResolution {
		return
	}

	//nolint:errcheck // diagnostic only; see doc comment
	_, _ = d.db.ExecContext(ctx,
		"UPDATE api_tokens SET last_used_at = ? WHERE token_hash = ?",
		now.Format(time.RFC3339Nano), tokenHash)
}

// ListAPITokens returns every stored token's metadata, newest first.
func (d *DB) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT name, scope, created_at, last_used_at
		FROM api_tokens
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing API tokens: %w", err)
	}
	defer rows.Close()

	var tokens []APIToken
	for rows.Next() {
		var (
			t        APIToken
			created  string
			lastUsed sql.NullString
		)
		if err := rows.Scan(&t.Name, &t.Scope, &created, &lastUsed); err != nil {
			return nil, fmt.Errorf("scanning API token: %w", err)
		}

		if t.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("parsing token created_at %q: %w", created, err)
		}
		if lastUsed.Valid {
			used, err := time.Parse(time.RFC3339Nano, lastUsed.String)
			if err != nil {
				return nil, fmt.Errorf("parsing token last_used_at: %w", err)
			}
			t.LastUsed = &used
		}

		tokens = append(tokens, t)
	}

	return tokens, rows.Err()
}

// DeleteAPIToken revokes a token by name, reporting whether one was removed.
func (d *DB) DeleteAPIToken(ctx context.Context, name string) (bool, error) {
	res, err := d.db.ExecContext(ctx, "DELETE FROM api_tokens WHERE name = ?", name)
	if err != nil {
		return false, fmt.Errorf("revoking API token: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("counting revoked tokens: %w", err)
	}
	return n > 0, nil
}
