package state

import (
	"database/sql"
	"fmt"
)

// migration is one forward step in the schema's history.
//
// Migrations are applied in order and recorded in schema_migrations, so each
// runs exactly once per database. They are never edited after release: a
// change to an already-applied migration would silently diverge existing
// installs from new ones. Corrections go in a new migration.
type migration struct {
	version int
	name    string
	stmts   []string
}

// migrations is the ordered schema history. Append only.
var migrations = []migration{
	{
		version: 1,
		name:    "initial schema",
		stmts: []string{
			`CREATE TABLE IF NOT EXISTS sequences (
				id TEXT PRIMARY KEY,
				plan_name TEXT NOT NULL,
				state TEXT NOT NULL,
				current_stage INTEGER NOT NULL DEFAULT 0,
				ponr_crossed INTEGER NOT NULL DEFAULT 0,
				started_at TEXT NOT NULL,
				completed_at TEXT,
				config_snapshot BLOB,
				pre_sequence_state TEXT,
				resolved_addrs TEXT
			)`,
			`CREATE TABLE IF NOT EXISTS client_states (
				client_name TEXT PRIMARY KEY,
				state TEXT NOT NULL,
				updated_at TEXT NOT NULL,
				sequence_id TEXT
			)`,
			`CREATE TABLE IF NOT EXISTS intents (
				id TEXT PRIMARY KEY,
				sequence_id TEXT NOT NULL,
				client_name TEXT NOT NULL,
				action TEXT NOT NULL,
				timestamp TEXT NOT NULL,
				status TEXT NOT NULL,
				result TEXT,
				FOREIGN KEY (sequence_id) REFERENCES sequences(id)
			)`,
			`CREATE TABLE IF NOT EXISTS stage_records (
				sequence_id TEXT NOT NULL,
				stage_index INTEGER NOT NULL,
				stage_name TEXT NOT NULL,
				started_at TEXT NOT NULL,
				completed_at TEXT,
				clients TEXT,
				PRIMARY KEY (sequence_id, stage_index),
				FOREIGN KEY (sequence_id) REFERENCES sequences(id)
			)`,
			`CREATE TABLE IF NOT EXISTS client_locks (
				client_name TEXT PRIMARY KEY,
				sequence_id TEXT NOT NULL,
				locked_at TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS api_tokens (
				token_hash TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				scope TEXT NOT NULL DEFAULT 'read',
				created_at TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS auth (
				id INTEGER PRIMARY KEY CHECK (id = 1),
				password_hash TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS kv (
				key TEXT PRIMARY KEY,
				value TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_intents_sequence ON intents(sequence_id)`,
			`CREATE INDEX IF NOT EXISTS idx_intents_client ON intents(client_name)`,
		},
	},
	{
		version: 2,
		name:    "sessions table",
		stmts: []string{
			// Sessions previously lived in the kv table keyed "session:<hash>"
			// with an RFC3339 string value, which made expiry pruning
			// impossible to express in SQL — so it never happened and the
			// table grew without bound. A dedicated table with an integer
			// expiry makes cleanup a single indexed DELETE.
			`CREATE TABLE IF NOT EXISTS sessions (
				token_hash TEXT PRIMARY KEY,
				created_at INTEGER NOT NULL,
				expires_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at)`,
			// Abandon any legacy session rows. They cannot be migrated
			// reliably and the only consequence is that current users log in
			// again.
			`DELETE FROM kv WHERE key LIKE 'session:%'`,
		},
	},
	{
		version: 3,
		name:    "drop unused dwell_state table",
		stmts: []string{
			// dwell_state was created by the original schema and never read or
			// written. Dwell state is now persisted by the conditions package
			// under its own schema; see migration 4.
			`DROP TABLE IF EXISTS dwell_state`,
		},
	},
	{
		version: 4,
		name:    "dwell tracker persistence",
		stmts: []string{
			// Dwell timers ("battery above 60% for 5 minutes") were held only
			// in memory, so every restart silently reset them to zero.
			// elapsed_ns records credit already earned; last_seen_at lets the
			// executor refuse to credit the gap across a restart.
			`CREATE TABLE IF NOT EXISTS dwell_trackers (
				condition_key TEXT PRIMARY KEY,
				required_ns INTEGER NOT NULL,
				elapsed_ns INTEGER NOT NULL DEFAULT 0,
				satisfied INTEGER NOT NULL DEFAULT 0,
				last_seen_at INTEGER NOT NULL
			)`,
		},
	},
	{
		version: 5,
		name:    "index sequences by state for active lookup",
		stmts: []string{
			// GetActiveSequence filters on state and orders by started_at on
			// every executor restart and every /api/status request.
			`CREATE INDEX IF NOT EXISTS idx_sequences_state_started
				ON sequences(state, started_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_stage_records_sequence
				ON stage_records(sequence_id)`,
		},
	},
	{
		version: 6,
		name:    "api token metadata",
		stmts: []string{
			// api_tokens has existed since the first schema and was checked
			// on every request, but nothing could ever insert a row: there
			// was no command, no endpoint and no caller of SaveAPIToken.
			// Names must be unique so revocation can address one.
			`ALTER TABLE api_tokens ADD COLUMN last_used_at TEXT`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_api_tokens_name ON api_tokens(name)`,
		},
	},
}

// migrate brings the database up to the latest schema version.
//
// Each migration runs inside its own transaction together with the bookkeeping
// row that records it, so a failure part way through leaves the database at
// the last fully applied version rather than in a half-migrated state.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	applied, err := appliedVersions(db)
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			return fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
		}
	}

	return nil
}

func appliedVersions(db *sql.DB) (map[int]bool, error) {
	rows, err := db.Query("SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("reading applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scanning applied migration: %w", err)
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func applyMigration(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once the tx is committed

	for i, stmt := range m.stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("statement %d: %w", i, err)
		}
	}

	if _, err := tx.Exec(
		"INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)",
		m.version, m.name, nowUnixString(),
	); err != nil {
		return fmt.Errorf("recording migration: %w", err)
	}

	return tx.Commit()
}

// SchemaVersion returns the highest applied migration version. Zero means an
// empty database.
func (d *DB) SchemaVersion() (int, error) {
	var version sql.NullInt64
	err := d.db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("reading schema version: %w", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}
