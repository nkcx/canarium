package state

import (
	"database/sql"
	"errors"
	"testing"
)

func TestMigrateIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	first, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	v1, err := first.SchemaVersion(t.Context())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	// Reopening must not re-run migrations or fail.
	second, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer second.Close()

	v2, err := second.SchemaVersion(t.Context())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v1 != v2 {
		t.Errorf("schema version changed on reopen: %d then %d", v1, v2)
	}
}

func TestMigrateReachesLatestVersion(t *testing.T) {
	db, err := Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	got, err := db.SchemaVersion(t.Context())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}

	want := migrations[len(migrations)-1].version
	if got != want {
		t.Errorf("schema version = %d, want %d", got, want)
	}
}

// TestMigrationVersionsAreSequentialAndUnique guards the append-only
// invariant: a duplicate or out-of-order version would cause migrations to be
// skipped or applied twice across installs.
func TestMigrationVersionsAreSequentialAndUnique(t *testing.T) {
	for i, m := range migrations {
		want := i + 1
		if m.version != want {
			t.Errorf("migrations[%d].version = %d, want %d (versions must start at 1 and increase by 1)",
				i, m.version, want)
		}
		if m.name == "" {
			t.Errorf("migrations[%d] (version %d) has no name", i, m.version)
		}
		if len(m.stmts) == 0 {
			t.Errorf("migrations[%d] (version %d) has no statements", i, m.version)
		}
	}
}

// TestMigrateRecordsEveryMigration verifies the bookkeeping table matches the
// declared history, so a partially applied upgrade is detectable.
func TestMigrateRecordsEveryMigration(t *testing.T) {
	db, err := Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	rows, err := db.db.Query("SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatalf("querying schema_migrations: %v", err)
	}
	defer rows.Close()

	var got []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scanning: %v", err)
		}
		got = append(got, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating: %v", err)
	}

	if len(got) != len(migrations) {
		t.Fatalf("recorded %d migrations, declared %d", len(got), len(migrations))
	}
	for i, v := range got {
		if v != migrations[i].version {
			t.Errorf("recorded migration %d = %d, want %d", i, v, migrations[i].version)
		}
	}
}

// TestMigrationFailureIsAtomic verifies a broken migration leaves the
// database at the previous version rather than half-applied.
func TestMigrationFailureIsAtomic(t *testing.T) {
	dir := t.TempDir()

	db, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	before, err := db.SchemaVersion(t.Context())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	db.Close()

	raw, err := sql.Open("sqlite", dir+"/state.db")
	if err != nil {
		t.Fatalf("opening raw: %v", err)
	}
	defer raw.Close()

	bad := migration{
		version: 9999,
		name:    "intentionally broken",
		stmts: []string{
			`CREATE TABLE should_not_survive (x INTEGER)`,
			`THIS IS NOT VALID SQL`,
		},
	}
	if err := applyMigration(t.Context(), raw, bad); err == nil {
		t.Fatal("applyMigration accepted invalid SQL")
	}

	// The first statement's table must have been rolled back with the rest.
	var name string
	err = raw.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='should_not_survive'`,
	).Scan(&name)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("partial migration was committed: found table %q (err=%v)", name, err)
	}

	var count int
	if err := raw.QueryRow(
		"SELECT COUNT(*) FROM schema_migrations WHERE version = ?", bad.version,
	).Scan(&count); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if count != 0 {
		t.Error("a failed migration was recorded as applied")
	}

	raw.Close()

	reopened, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("reopening after failed migration: %v", err)
	}
	defer reopened.Close()

	after, err := reopened.SchemaVersion(t.Context())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if after != before {
		t.Errorf("schema version = %d after a failed migration, want %d", after, before)
	}
}
