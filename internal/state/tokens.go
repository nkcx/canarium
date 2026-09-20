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

// ValidateAPIToken returns the scope for a token digest, or "" if unknown.
//
// It also records the use, so an operator can tell which tokens are live
// before revoking one.
func (d *DB) ValidateAPIToken(ctx context.Context, tokenHash string) (string, error) {
	var scope string
	err := d.db.QueryRowContext(ctx,
		"SELECT scope FROM api_tokens WHERE token_hash = ?", tokenHash).Scan(&scope)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("validating API token: %w", err)
	}

	if _, err := d.db.ExecContext(ctx,
		"UPDATE api_tokens SET last_used_at = ? WHERE token_hash = ?",
		time.Now().Format(time.RFC3339Nano), tokenHash,
	); err != nil {
		// Deliberately not fatal. last_used_at is diagnostic: it tells an
		// operator which tokens are live before they revoke one. Failing
		// authentication because that bookkeeping write failed would turn a
		// full disk into an outage.
		//
		//nolint:nilerr // the token is valid regardless of this write
		return scope, nil
	}

	return scope, nil
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
