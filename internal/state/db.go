package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	// Pure-Go SQLite driver: no cgo, so the binary cross-compiles to every
	// target in the Makefile dist set and links statically.
	_ "modernc.org/sqlite"
)

type DB struct {
	db *sql.DB
}

const (
	// dataDirMode keeps the state directory private to the daemon's user.
	// It holds the admin password hash, live session tokens and API token
	// digests; the previous 0755 made all of that world-readable.
	dataDirMode = 0o700

	// dbFileMode likewise restricts the database file itself.
	dbFileMode = 0o600

	// busyTimeoutMS is how long a writer waits for a competing one.
	busyTimeoutMS = 5000
)

func Open(ctx context.Context, dataDir string) (*DB, error) {
	if err := os.MkdirAll(dataDir, dataDirMode); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "state.db")
	dsn := dbPath + "?" + strings.Join([]string{
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		fmt.Sprintf("_pragma=busy_timeout(%d)", busyTimeoutMS),
		"_pragma=foreign_keys(ON)",
	}, "&")

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// SQLite permits one writer at a time. Letting database/sql open an
	// unbounded pool means concurrent writers — a stage shutting down eight
	// clients at once, each recording an intent — contend for the write lock
	// and rely on busy_timeout to sort it out. Serialising writes in the
	// pool avoids the contention entirely.
	//
	// Reads would benefit from a separate pool, but the write volume here is
	// a handful of rows per sequence; the simplicity is worth more.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close() // the open failed; the close error adds nothing
		return nil, fmt.Errorf("opening database at %s: %w", dbPath, err)
	}

	if err := migrate(ctx, db); err != nil {
		_ = db.Close() // already failing; report the migration error
		return nil, fmt.Errorf("migrating database: %w", err)
	}

	// Applied after creation, since the file does not exist until the first
	// connection. WAL mode creates -wal and -shm siblings that inherit the
	// directory's permissions.
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Chmod(path, dbFileMode); err != nil && !os.IsNotExist(err) {
			_ = db.Close() // already failing; report the chmod error
			return nil, fmt.Errorf("restricting permissions on %s: %w", path, err)
		}
	}

	return &DB{db: db}, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) SaveSequence(ctx context.Context, seq *Sequence) error {
	preState, _ := json.Marshal(seq.PreSequenceState)
	resolved, _ := json.Marshal(seq.ResolvedAddrs)

	var completedAt *string
	if seq.CompletedAt != nil {
		s := seq.CompletedAt.Format(time.RFC3339Nano)
		completedAt = &s
	}

	_, err := d.db.ExecContext(ctx, `
		INSERT INTO sequences (id, plan_name, state, current_stage, ponr_crossed, started_at, completed_at, config_snapshot, pre_sequence_state, resolved_addrs)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			state=excluded.state,
			current_stage=excluded.current_stage,
			ponr_crossed=excluded.ponr_crossed,
			completed_at=excluded.completed_at
	`, seq.ID, seq.PlanName, seq.State, seq.CurrentStage, seq.PonrCrossed,
		seq.StartedAt.Format(time.RFC3339Nano), completedAt,
		seq.ConfigSnapshot, string(preState), string(resolved))
	return err
}

func (d *DB) GetActiveSequence(ctx context.Context) (*Sequence, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, plan_name, state, current_stage, ponr_crossed, started_at, completed_at, config_snapshot, pre_sequence_state, resolved_addrs
		FROM sequences
		WHERE state NOT IN ('completed', 'failed', 'idle')
		ORDER BY started_at DESC
		LIMIT 1
	`)

	return scanSequence(row)
}

// scanSequence decodes one sequences row.
func scanSequence(row *sql.Row) (*Sequence, error) {
	var seq Sequence
	var startedAt, preState, resolved string
	var completedAt, configSnapshot *string

	err := row.Scan(&seq.ID, &seq.PlanName, &seq.State, &seq.CurrentStage, &seq.PonrCrossed,
		&startedAt, &completedAt, &configSnapshot, &preState, &resolved)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scanning sequence: %w", err)
	}

	seq.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return nil, fmt.Errorf("parsing sequence started_at %q: %w", startedAt, err)
	}
	if completedAt != nil {
		t, err := time.Parse(time.RFC3339Nano, *completedAt)
		if err != nil {
			return nil, fmt.Errorf("parsing sequence completed_at %q: %w", *completedAt, err)
		}
		seq.CompletedAt = &t
	}
	if configSnapshot != nil {
		seq.ConfigSnapshot = []byte(*configSnapshot)
	}
	if err := json.Unmarshal([]byte(preState), &seq.PreSequenceState); err != nil {
		return nil, fmt.Errorf("decoding pre_sequence_state: %w", err)
	}
	if err := json.Unmarshal([]byte(resolved), &seq.ResolvedAddrs); err != nil {
		return nil, fmt.Errorf("decoding resolved_addrs: %w", err)
	}

	return &seq, nil
}

// LastSequence returns the most recently started sequence, or nil if none
// has ever run.
func (d *DB) LastSequence(ctx context.Context) (*Sequence, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, plan_name, state, current_stage, ponr_crossed, started_at,
		       completed_at, config_snapshot, pre_sequence_state, resolved_addrs
		FROM sequences
		ORDER BY started_at DESC
		LIMIT 1
	`)
	return scanSequence(row)
}

func (d *DB) SaveClientState(ctx context.Context, name, state string, sequenceID *string) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO client_states (client_name, state, updated_at, sequence_id)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(client_name) DO UPDATE SET
			state=excluded.state,
			updated_at=excluded.updated_at,
			sequence_id=excluded.sequence_id
	`, name, state, time.Now().Format(time.RFC3339Nano), sequenceID)
	return err
}

func (d *DB) GetClientState(ctx context.Context, name string) (string, error) {
	var state string
	err := d.db.QueryRowContext(ctx, "SELECT state FROM client_states WHERE client_name = ?", name).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "unknown", nil
	}
	return state, err
}

func (d *DB) GetAllClientStates(ctx context.Context) (map[string]string, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT client_name, state FROM client_states")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var name, state string
		if err := rows.Scan(&name, &state); err != nil {
			return nil, err
		}
		result[name] = state
	}
	return result, rows.Err()
}

func (d *DB) SaveIntent(ctx context.Context, intent *Intent) error {
	var result *string
	if intent.Result != nil {
		b, _ := json.Marshal(intent.Result)
		s := string(b)
		result = &s
	}

	_, err := d.db.ExecContext(ctx, `
		INSERT INTO intents (id, sequence_id, client_name, action, timestamp, status, result)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status=excluded.status,
			result=excluded.result
	`, intent.ID, intent.SequenceID, intent.ClientName, intent.Action,
		intent.Timestamp.Format(time.RFC3339Nano), intent.Status, result)
	return err
}

func (d *DB) SaveStageRecord(ctx context.Context, rec *StageRecord) error {
	clients, _ := json.Marshal(rec.Clients)
	var completedAt *string
	if rec.CompletedAt != nil {
		s := rec.CompletedAt.Format(time.RFC3339Nano)
		completedAt = &s
	}

	_, err := d.db.ExecContext(ctx, `
		INSERT INTO stage_records (sequence_id, stage_index, stage_name, started_at, completed_at, clients)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(sequence_id, stage_index) DO UPDATE SET
			completed_at=excluded.completed_at,
			clients=excluded.clients
	`, rec.SequenceID, rec.StageIndex, rec.StageName,
		rec.StartedAt.Format(time.RFC3339Nano), completedAt, string(clients))
	return err
}

func (d *DB) GetCompletedStages(ctx context.Context, sequenceID string) ([]int, error) {
	rows, err := d.db.QueryContext(ctx,
		"SELECT stage_index FROM stage_records WHERE sequence_id = ? AND completed_at IS NOT NULL ORDER BY stage_index",
		sequenceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stages []int
	for rows.Next() {
		var idx int
		if err := rows.Scan(&idx); err != nil {
			return nil, err
		}
		stages = append(stages, idx)
	}
	return stages, rows.Err()
}

// AcquireClientLock claims exclusive control of a client for a sequence.
//
// The lock is re-entrant for the sequence that already holds it. Without
// that, a sequence resuming after a restart could not reclaim its own locks:
// the rows survive the crash, the insert conflicts, and every client is
// skipped with "locked by another sequence" — so a resumed sequence would
// silently shut nothing down.
//
// Returns false only when a *different* sequence holds the lock.
func (d *DB) AcquireClientLock(ctx context.Context, clientName, sequenceID string) (bool, error) {
	res, err := d.db.ExecContext(ctx, `
		INSERT INTO client_locks (client_name, sequence_id, locked_at)
		VALUES (?, ?, ?)
		ON CONFLICT(client_name) DO UPDATE SET
			locked_at = excluded.locked_at
		WHERE client_locks.sequence_id = excluded.sequence_id
	`, clientName, sequenceID, time.Now().Format(time.RFC3339Nano))
	if err != nil {
		return false, fmt.Errorf("acquiring client lock: %w", err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking client lock: %w", err)
	}
	return rows > 0, nil
}

// ClientLockHolder returns the sequence holding a client's lock, or "" if it
// is unlocked.
func (d *DB) ClientLockHolder(ctx context.Context, clientName string) (string, error) {
	var seqID string
	err := d.db.QueryRowContext(ctx,
		"SELECT sequence_id FROM client_locks WHERE client_name = ?", clientName).Scan(&seqID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading client lock: %w", err)
	}
	return seqID, nil
}

func (d *DB) ReleaseClientLock(ctx context.Context, clientName string) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM client_locks WHERE client_name = ?", clientName)
	return err
}

func (d *DB) ReleaseSequenceLocks(ctx context.Context, sequenceID string) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM client_locks WHERE sequence_id = ?", sequenceID)
	return err
}

func (d *DB) SetKV(ctx context.Context, key, value string) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO kv (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value
	`, key, value)
	return err
}

func (d *DB) GetKV(ctx context.Context, key string) (string, error) {
	var value string
	err := d.db.QueryRowContext(ctx, "SELECT value FROM kv WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (d *DB) SetPasswordHash(ctx context.Context, hash string) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO auth (id, password_hash) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET password_hash=excluded.password_hash
	`, hash)
	return err
}

func (d *DB) GetPasswordHash(ctx context.Context) (string, error) {
	var hash string
	err := d.db.QueryRowContext(ctx, "SELECT password_hash FROM auth WHERE id = 1").Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return hash, err
}

type Sequence struct {
	ID               string
	PlanName         string
	State            string
	CurrentStage     int
	PonrCrossed      bool
	StartedAt        time.Time
	CompletedAt      *time.Time
	ConfigSnapshot   []byte
	PreSequenceState map[string]string
	ResolvedAddrs    map[string]ResolvedAddr
}

type ResolvedAddr struct {
	IP         string `json:"ip"`
	MAC        string `json:"mac"`
	Hostname   string `json:"hostname"`
	ResolvedAt string `json:"resolved_at"`
}

type Intent struct {
	ID         string
	SequenceID string
	ClientName string
	Action     string
	Timestamp  time.Time
	Status     string
	Result     *ActionResult
}

type ActionResult struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

type StageRecord struct {
	SequenceID  string
	StageIndex  int
	StageName   string
	StartedAt   time.Time
	CompletedAt *time.Time
	Clients     map[string]ClientResult
}

type ClientResult struct {
	State       string `json:"state"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at,omitempty"`
	Error       string `json:"error,omitempty"`
}

// nowUnixString renders the current time as Unix seconds for the schema
// columns that store integer timestamps.
func nowUnixString() string {
	return strconv.FormatInt(time.Now().Unix(), 10)
}
