package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	db *sql.DB
}

func Open(dataDir string) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "state.db")
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating database: %w", err)
	}

	return &DB{db: db}, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS sequences (
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
		);

		CREATE TABLE IF NOT EXISTS client_states (
			client_name TEXT PRIMARY KEY,
			state TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			sequence_id TEXT
		);

		CREATE TABLE IF NOT EXISTS intents (
			id TEXT PRIMARY KEY,
			sequence_id TEXT NOT NULL,
			client_name TEXT NOT NULL,
			action TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			status TEXT NOT NULL,
			result TEXT,
			FOREIGN KEY (sequence_id) REFERENCES sequences(id)
		);

		CREATE TABLE IF NOT EXISTS stage_records (
			sequence_id TEXT NOT NULL,
			stage_index INTEGER NOT NULL,
			stage_name TEXT NOT NULL,
			started_at TEXT NOT NULL,
			completed_at TEXT,
			clients TEXT,
			PRIMARY KEY (sequence_id, stage_index),
			FOREIGN KEY (sequence_id) REFERENCES sequences(id)
		);

		CREATE TABLE IF NOT EXISTS dwell_state (
			condition_key TEXT PRIMARY KEY,
			required_ns INTEGER NOT NULL,
			first_true TEXT,
			last_true TEXT,
			satisfied INTEGER NOT NULL DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS client_locks (
			client_name TEXT PRIMARY KEY,
			sequence_id TEXT NOT NULL,
			locked_at TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS api_tokens (
			token_hash TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			scope TEXT NOT NULL DEFAULT 'read',
			created_at TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS auth (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			password_hash TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS kv (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_intents_sequence ON intents(sequence_id);
		CREATE INDEX IF NOT EXISTS idx_intents_client ON intents(client_name);
	`)
	return err
}

func (d *DB) SaveSequence(seq *Sequence) error {
	preState, _ := json.Marshal(seq.PreSequenceState)
	resolved, _ := json.Marshal(seq.ResolvedAddrs)

	var completedAt *string
	if seq.CompletedAt != nil {
		s := seq.CompletedAt.Format(time.RFC3339Nano)
		completedAt = &s
	}

	_, err := d.db.Exec(`
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

func (d *DB) GetActiveSequence() (*Sequence, error) {
	row := d.db.QueryRow(`
		SELECT id, plan_name, state, current_stage, ponr_crossed, started_at, completed_at, config_snapshot, pre_sequence_state, resolved_addrs
		FROM sequences
		WHERE state NOT IN ('completed', 'failed', 'idle')
		ORDER BY started_at DESC
		LIMIT 1
	`)

	var seq Sequence
	var startedAt, preState, resolved string
	var completedAt, configSnapshot *string

	err := row.Scan(&seq.ID, &seq.PlanName, &seq.State, &seq.CurrentStage, &seq.PonrCrossed,
		&startedAt, &completedAt, &configSnapshot, &preState, &resolved)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	seq.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
	if completedAt != nil {
		t, _ := time.Parse(time.RFC3339Nano, *completedAt)
		seq.CompletedAt = &t
	}
	if configSnapshot != nil {
		seq.ConfigSnapshot = []byte(*configSnapshot)
	}
	json.Unmarshal([]byte(preState), &seq.PreSequenceState)
	json.Unmarshal([]byte(resolved), &seq.ResolvedAddrs)

	return &seq, nil
}

func (d *DB) SaveClientState(name, state string, sequenceID *string) error {
	_, err := d.db.Exec(`
		INSERT INTO client_states (client_name, state, updated_at, sequence_id)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(client_name) DO UPDATE SET
			state=excluded.state,
			updated_at=excluded.updated_at,
			sequence_id=excluded.sequence_id
	`, name, state, time.Now().Format(time.RFC3339Nano), sequenceID)
	return err
}

func (d *DB) GetClientState(name string) (string, error) {
	var state string
	err := d.db.QueryRow("SELECT state FROM client_states WHERE client_name = ?", name).Scan(&state)
	if err == sql.ErrNoRows {
		return "unknown", nil
	}
	return state, err
}

func (d *DB) GetAllClientStates() (map[string]string, error) {
	rows, err := d.db.Query("SELECT client_name, state FROM client_states")
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

func (d *DB) SaveIntent(intent *Intent) error {
	var result *string
	if intent.Result != nil {
		b, _ := json.Marshal(intent.Result)
		s := string(b)
		result = &s
	}

	_, err := d.db.Exec(`
		INSERT INTO intents (id, sequence_id, client_name, action, timestamp, status, result)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status=excluded.status,
			result=excluded.result
	`, intent.ID, intent.SequenceID, intent.ClientName, intent.Action,
		intent.Timestamp.Format(time.RFC3339Nano), intent.Status, result)
	return err
}

func (d *DB) SaveStageRecord(rec *StageRecord) error {
	clients, _ := json.Marshal(rec.Clients)
	var completedAt *string
	if rec.CompletedAt != nil {
		s := rec.CompletedAt.Format(time.RFC3339Nano)
		completedAt = &s
	}

	_, err := d.db.Exec(`
		INSERT INTO stage_records (sequence_id, stage_index, stage_name, started_at, completed_at, clients)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(sequence_id, stage_index) DO UPDATE SET
			completed_at=excluded.completed_at,
			clients=excluded.clients
	`, rec.SequenceID, rec.StageIndex, rec.StageName,
		rec.StartedAt.Format(time.RFC3339Nano), completedAt, string(clients))
	return err
}

func (d *DB) GetCompletedStages(sequenceID string) ([]int, error) {
	rows, err := d.db.Query(
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

func (d *DB) AcquireClientLock(clientName, sequenceID string) (bool, error) {
	result, err := d.db.Exec(`
		INSERT INTO client_locks (client_name, sequence_id, locked_at)
		VALUES (?, ?, ?)
		ON CONFLICT(client_name) DO NOTHING
	`, clientName, sequenceID, time.Now().Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	rows, _ := result.RowsAffected()
	return rows > 0, nil
}

func (d *DB) ReleaseClientLock(clientName string) error {
	_, err := d.db.Exec("DELETE FROM client_locks WHERE client_name = ?", clientName)
	return err
}

func (d *DB) ReleaseSequenceLocks(sequenceID string) error {
	_, err := d.db.Exec("DELETE FROM client_locks WHERE sequence_id = ?", sequenceID)
	return err
}

func (d *DB) SetKV(key, value string) error {
	_, err := d.db.Exec(`
		INSERT INTO kv (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value
	`, key, value)
	return err
}

func (d *DB) GetKV(key string) (string, error) {
	var value string
	err := d.db.QueryRow("SELECT value FROM kv WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (d *DB) SetPasswordHash(hash string) error {
	_, err := d.db.Exec(`
		INSERT INTO auth (id, password_hash) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET password_hash=excluded.password_hash
	`, hash)
	return err
}

func (d *DB) GetPasswordHash() (string, error) {
	var hash string
	err := d.db.QueryRow("SELECT password_hash FROM auth WHERE id = 1").Scan(&hash)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return hash, err
}

func (d *DB) SaveAPIToken(tokenHash, name, scope string) error {
	_, err := d.db.Exec(`
		INSERT INTO api_tokens (token_hash, name, scope, created_at)
		VALUES (?, ?, ?, ?)
	`, tokenHash, name, scope, time.Now().Format(time.RFC3339Nano))
	return err
}

func (d *DB) ValidateAPIToken(tokenHash string) (string, error) {
	var scope string
	err := d.db.QueryRow("SELECT scope FROM api_tokens WHERE token_hash = ?", tokenHash).Scan(&scope)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return scope, err
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
