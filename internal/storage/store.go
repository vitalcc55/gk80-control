// Package storage owns the demonstration profile's SQLite records.
package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	_ "modernc.org/sqlite"
)

var (
	ErrCommandConflict    = errors.New("command identifier already used")
	ErrRevision           = errors.New("revision mismatch")
	ErrActiveTest         = errors.New("another test is active")
	ErrInvalidCommand     = errors.New("invalid create command")
	ErrStorageUnavailable = errors.New("mandatory storage unavailable")
	ErrInvalidState       = errors.New("invalid test state")
	ErrActionMismatch     = errors.New("pending action does not match")
	ErrTestNotFound       = errors.New("test not found")
	ErrCommandNotFound    = errors.New("command not found")
	ErrNotReady           = errors.New("simulator not ready")
	ErrWriteNotStarted    = errors.New("command write did not start")
)

var inputValidator = sync.OnceValues(contract.NewInputValidator)

const operatorID = "demo-operator"

// Store is opened only while the core owns the corresponding profile lock.
type Store struct {
	db         *sql.DB
	writeMu    chan struct{}
	failed     bool
	settings   SQLiteSettings
	databaseID uuid.UUID
}

func (s *Store) lockWrite(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.writeMu <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) unlockWrite() { <-s.writeMu }

type SQLiteSettings struct {
	Version     string
	JournalMode string
	Synchronous string
}

func Open(profileDir string) (*Store, error) {
	if !filepath.IsAbs(profileDir) {
		return nil, errors.New("profile directory must be absolute")
	}
	path := filepath.ToSlash(filepath.Join(profileDir, "demo.sqlite"))
	var address url.URL
	if strings.HasPrefix(path, "//") {
		parts := strings.SplitN(strings.TrimPrefix(path, "//"), "/", 2)
		if len(parts) != 2 {
			return nil, errors.New("invalid profile path")
		}
		address = url.URL{Scheme: "file", Host: parts[0], Path: "/" + parts[1]}
	} else {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		address = url.URL{Scheme: "file", Path: path}
	}
	query := url.Values{"_foreign_keys": {"on"}, "_synchronous": {"full"}}
	address.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", address.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, writeMu: make(chan struct{}, 1)}
	if err := store.initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.configure(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) configure() error {
	var mode, version string
	var synchronous, foreignKeys int
	if err := s.db.QueryRow(`PRAGMA journal_mode = WAL`).Scan(&mode); err != nil {
		return err
	}
	if err := s.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		return err
	}
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return err
	}
	if err := s.db.QueryRow(`SELECT sqlite_version()`).Scan(&version); err != nil {
		return err
	}
	if mode != "wal" || synchronous != 2 || foreignKeys != 1 || version == "" {
		return errors.New("required SQLite settings unavailable")
	}
	s.settings = SQLiteSettings{Version: version, JournalMode: mode, Synchronous: "FULL"}
	return nil
}

func (s *Store) Settings() SQLiteSettings { return s.settings }

// DatabaseID identifies the SQLite database independently of the core process.
func (s *Store) DatabaseID() uuid.UUID { return s.databaseID }

func (s *Store) initialize() error {
	if _, err := s.db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return err
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version == 3 {
		id, err := readDatabaseID(s.db)
		if err != nil {
			return err
		}
		s.databaseID = id
		return nil
	}
	if version != 0 && version != 1 && version != 2 {
		return errors.New("unsupported database schema")
	}
	if version == 0 {
		var objects int
		if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&objects); err != nil {
			return err
		}
		if objects != 0 {
			return errors.New("unrecognized database")
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var statements []string
	if version == 0 {
		statements = []string{
			`CREATE TABLE meta (id INTEGER PRIMARY KEY CHECK (id = 1), revision INTEGER NOT NULL CHECK (revision >= 0))`,
			`INSERT INTO meta (id, revision) VALUES (1, 0)`,
			`CREATE TABLE tests (id TEXT PRIMARY KEY, record BLOB NOT NULL, stage TEXT NOT NULL)`,
			`CREATE TABLE commands (id TEXT PRIMARY KEY, operation TEXT NOT NULL, request BLOB NOT NULL, status TEXT NOT NULL, test_id TEXT REFERENCES tests(id), reason TEXT, operator_id TEXT NOT NULL, occurred_at TEXT NOT NULL)`,
			`CREATE TABLE audit (sequence INTEGER PRIMARY KEY AUTOINCREMENT, test_id TEXT NOT NULL REFERENCES tests(id), occurred_at TEXT NOT NULL, operator_id TEXT NOT NULL, action TEXT NOT NULL, outcome TEXT NOT NULL, revision INTEGER NOT NULL, command_id TEXT, action_id TEXT, reason TEXT)`,
			`CREATE INDEX audit_by_test ON audit(test_id, sequence)`,
		}
	}
	if version < 2 {
		statements = append(statements,
			`CREATE TABLE test_actions (action_id TEXT PRIMARY KEY, test_id TEXT NOT NULL REFERENCES tests(id), instruction TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('pending', 'confirmed', 'canceled', 'unknown')))`,
			`CREATE UNIQUE INDEX one_pending_action ON test_actions(test_id) WHERE state = 'pending'`,
			`CREATE TABLE measurements (test_id TEXT NOT NULL REFERENCES tests(id), sequence INTEGER NOT NULL CHECK(sequence > 0), elapsed_ns INTEGER NOT NULL CHECK(elapsed_ns >= 0), channel TEXT NOT NULL, quality TEXT NOT NULL, payload BLOB NOT NULL, PRIMARY KEY(test_id, sequence))`,
			`ALTER TABLE tests ADD COLUMN measurement_elapsed_ns INTEGER`,
		)
	}
	statements = append(statements, `ALTER TABLE meta ADD COLUMN database_id TEXT`)
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	result, err := tx.Exec(`UPDATE meta SET database_id = ? WHERE id = 1`, id.String())
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return errors.New("invalid database metadata")
	}
	if _, err := tx.Exec(`PRAGMA user_version = 3`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.databaseID = id
	return nil
}

func readDatabaseID(db *sql.DB) (uuid.UUID, error) {
	var value string
	if err := db.QueryRow(`SELECT database_id FROM meta WHERE id = 1`).Scan(&value); err != nil {
		return uuid.Nil, errors.New("database identity unavailable")
	}
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil || id.String() != value || id.Version() != 4 || id.Variant() != uuid.RFC4122 {
		return uuid.Nil, errors.New("invalid database identity")
	}
	return id, nil
}

func (s *Store) Close() error { return s.db.Close() }

func valid(command contract.CreateTestCommand) bool {
	p := command.Parameters
	number := func(v float64) bool { return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v) }
	return command.CommandId != uuid.Nil && command.ExpectedRevision >= 0 && strings.TrimSpace(p.Valve) != "" && utf8.RuneCountInString(p.Valve) <= 200 && p.Direction == "pressure" && !p.Actuator && number(p.AreaM2) && number(p.PmaxPa) && number(p.MeasurementSeconds) && p.MeasurementSeconds <= 3600
}

// LookupCreate resolves an uncertain response without re-executing the command.
func (s *Store) LookupCreate(ctx context.Context, command contract.CreateTestCommand) (contract.CommandStatus, error) {
	request, err := json.Marshal(command)
	if err != nil {
		return contract.CommandStatus{}, ErrInvalidCommand
	}
	return s.lookupCreateRequest(ctx, command.CommandId, request)
}

func (s *Store) lookupCreateRequest(ctx context.Context, id uuid.UUID, request []byte) (contract.CommandStatus, error) {
	status, err := s.lookup(ctx, id, "create", request)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.CommandStatus{}, ErrCommandNotFound
	}
	return status, err
}

// LookupCreateExact resolves a structurally valid original request without executing it.
func (s *Store) LookupCreateExact(ctx context.Context, command contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	if _, err := validateCreateIdentity(command, identity); err != nil {
		return contract.CommandStatus{}, err
	}
	return s.lookupCreateRequest(ctx, command.CommandId, identity)
}

func validateCreateIdentity(command contract.CreateTestCommand, identity []byte) (bool, error) {
	validator, err := inputValidator()
	if err != nil {
		return false, err
	}
	var parsed contract.CreateTestCommand
	input, err := validator.InspectCanonical("CreateTestCommand", identity, &parsed)
	if err != nil || parsed != command || !bytes.Equal(identity, input.Identity) {
		return false, ErrInvalidCommand
	}
	return input.Valid, nil
}

// Create commits the immutable parameters, command, reserved stop ID and audit together.
func (s *Store) Create(ctx context.Context, command contract.CreateTestCommand) (contract.CommandStatus, error) {
	if command.CommandId == uuid.Nil {
		return contract.CommandStatus{}, ErrInvalidCommand
	}
	request, marshalErr := json.Marshal(command)
	return s.createRequest(ctx, command, request, marshalErr)
}

// CreateExact stores the canonical original request atomically with the command.
func (s *Store) CreateExact(ctx context.Context, command contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	valid, err := validateCreateIdentity(command, identity)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	if !valid {
		return s.createRequest(ctx, command, identity, ErrInvalidCommand)
	}
	return s.createRequest(ctx, command, identity, nil)
}

func (s *Store) createRequest(ctx context.Context, command contract.CreateTestCommand, request []byte, marshalErr error) (contract.CommandStatus, error) {
	if command.CommandId == uuid.Nil {
		return contract.CommandStatus{}, ErrInvalidCommand
	}
	if err := s.lockWrite(ctx); err != nil {
		return contract.CommandStatus{}, err
	}
	defer s.unlockWrite()
	if err := ctx.Err(); err != nil {
		return contract.CommandStatus{}, err
	}
	status, err := s.lookup(ctx, command.CommandId, "create", request)
	if err == nil || errors.Is(err, ErrCommandConflict) || errors.Is(err, ErrRevision) || errors.Is(err, ErrActiveTest) || errors.Is(err, ErrInvalidCommand) {
		return status, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		if ctx.Err() == nil {
			s.failed = true
		}
		return contract.CommandStatus{}, err
	}
	if s.failed {
		return contract.CommandStatus{}, ErrStorageUnavailable
	}
	if marshalErr != nil {
		return contract.CommandStatus{}, ErrInvalidCommand
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		if ctx.Err() == nil {
			s.failed = true
		}
		return contract.CommandStatus{}, err
	}
	defer func() { _ = tx.Rollback() }()
	wrote := false
	status, err = s.create(ctx, tx, command, request, &wrote)
	s.noteWriteError(ctx, err, false)
	return status, err
}

func (s *Store) noteWriteError(ctx context.Context, err error, writeAttempted bool) {
	if err == nil || errors.Is(err, ErrRevision) || errors.Is(err, ErrActiveTest) || errors.Is(err, ErrInvalidCommand) || errors.Is(err, ErrInvalidState) || errors.Is(err, ErrActionMismatch) || errors.Is(err, ErrTestNotFound) || errors.Is(err, ErrNotReady) {
		return
	}
	if !writeAttempted && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || (ctx.Err() != nil && errors.Is(err, sql.ErrTxDone))) {
		return
	}
	s.failed = true
}

func (s *Store) lookup(ctx context.Context, id uuid.UUID, expectedOperation string, request []byte) (contract.CommandStatus, error) {
	var operation, state string
	var testID, reason sql.NullString
	var stored []byte
	err := s.db.QueryRowContext(ctx, `SELECT operation, request, status, test_id, reason FROM commands WHERE id = ?`, id.String()).Scan(&operation, &stored, &state, &testID, &reason)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	if operation != expectedOperation {
		return contract.CommandStatus{}, ErrCommandConflict
	}
	if !bytes.Equal(stored, request) {
		if expectedOperation != "create" {
			return contract.CommandStatus{}, ErrCommandConflict
		}
		storedIdentity, err := contract.CanonicalJSON(stored)
		if errors.Is(err, contract.ErrIdentityTooLarge) {
			return contract.CommandStatus{}, ErrCommandConflict
		}
		if err != nil {
			return contract.CommandStatus{}, errors.New("invalid stored create request")
		}
		requestIdentity, err := contract.CanonicalJSON(request)
		if err != nil {
			return contract.CommandStatus{}, ErrInvalidCommand
		}
		if !bytes.Equal(storedIdentity, requestIdentity) {
			return contract.CommandStatus{}, ErrCommandConflict
		}
	}
	status := contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatus(state)}
	if testID.Valid {
		parsed, err := uuid.Parse(testID.String)
		if err != nil {
			return contract.CommandStatus{}, err
		}
		status.TestId = &parsed
	}
	if reason.Valid {
		status.Reason = &reason.String
		if expectedOperation == "start" && state == "failed" && reason.String == "canceled_by_stop" {
			return status, nil
		}
		switch reason.String {
		case "revision":
			return status, ErrRevision
		case "active_test":
			return status, ErrActiveTest
		case "invalid_command":
			return status, ErrInvalidCommand
		case "invalid_state":
			return status, ErrInvalidState
		case "action_mismatch":
			return status, ErrActionMismatch
		case "test_not_found":
			return status, ErrTestNotFound
		case "not_ready":
			return status, ErrNotReady
		default:
			return contract.CommandStatus{}, errors.New("invalid stored command reason")
		}
	}
	return status, nil
}

func reject(ctx context.Context, tx *sql.Tx, command contract.CreateTestCommand, request []byte, reason string, rejection error, writeAttempted *bool) (contract.CommandStatus, error) {
	if err := ctx.Err(); err != nil {
		return contract.CommandStatus{}, err
	}
	*writeAttempted = true
	if _, err := tx.ExecContext(ctx, `INSERT INTO commands(id, operation, request, status, reason, operator_id, occurred_at) VALUES(?, 'create', ?, 'rejected', ?, ?, ?)`, command.CommandId.String(), request, reason, operatorID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return contract.CommandStatus{}, err
	}
	if err := tx.Commit(); err != nil {
		return contract.CommandStatus{}, err
	}
	return contract.CommandStatus{CommandId: command.CommandId, Status: "rejected", Reason: &reason}, rejection
}

func (s *Store) create(ctx context.Context, tx *sql.Tx, command contract.CreateTestCommand, request []byte, writeAttempted *bool) (contract.CommandStatus, error) {
	if !valid(command) || !validDuration(request) {
		return reject(ctx, tx, command, request, "invalid_command", ErrInvalidCommand, writeAttempted)
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM meta WHERE id = 1`).Scan(&revision); err != nil {
		return contract.CommandStatus{}, err
	}
	if revision != command.ExpectedRevision {
		return reject(ctx, tx, command, request, "revision", ErrRevision, writeAttempted)
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tests WHERE stage != 'finished'`).Scan(&active); err != nil {
		return contract.CommandStatus{}, err
	}
	if active != 0 {
		return reject(ctx, tx, command, request, "active_test", ErrActiveTest, writeAttempted)
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return contract.CommandStatus{}, err
	}
	stopID, err := uuid.NewRandom()
	if err != nil {
		return contract.CommandStatus{}, err
	}
	record := contract.TestRecord{TestId: id, StopCommandId: stopID, Demo: true, OperatorId: operatorID, Parameters: command.Parameters, Stage: "preparing", Outcome: "pending", DataQuality: "pending", SaveStatus: "saved", CreatedAt: time.Now().UTC()}
	body, err := json.Marshal(record)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	if err := ctx.Err(); err != nil {
		return contract.CommandStatus{}, err
	}
	*writeAttempted = true
	if _, err := tx.ExecContext(ctx, `INSERT INTO tests(id, record, stage) VALUES(?, ?, ?)`, id.String(), body, record.Stage); err != nil {
		return contract.CommandStatus{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO commands(id, operation, request, status, test_id, operator_id, occurred_at) VALUES(?, 'create', ?, 'completed', ?, ?, ?)`, command.CommandId.String(), request, id.String(), operatorID, record.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return contract.CommandStatus{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO commands(id, operation, request, status, test_id, operator_id, occurred_at) VALUES(?, 'stop', x'', 'reserved', ?, ?, ?)`, stopID.String(), id.String(), operatorID, record.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return contract.CommandStatus{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit(test_id, occurred_at, operator_id, action, outcome, revision, command_id) VALUES(?, ?, ?, 'create', 'completed', ?, ?)`, id.String(), record.CreatedAt.Format(time.RFC3339Nano), operatorID, revision+1, command.CommandId.String()); err != nil {
		return contract.CommandStatus{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meta SET revision = ? WHERE id = 1`, revision+1); err != nil {
		return contract.CommandStatus{}, err
	}
	if err := tx.Commit(); err != nil {
		return contract.CommandStatus{}, err
	}
	return contract.CommandStatus{CommandId: command.CommandId, Status: "completed", TestId: &id}, nil
}

func validDuration(request []byte) bool {
	var body struct {
		Parameters struct {
			MeasurementSeconds json.Number `json:"measurementSeconds"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(request, &body); err != nil {
		return false
	}
	value, ok := new(big.Rat).SetString(body.Parameters.MeasurementSeconds.String())
	return ok && value.Sign() > 0 && value.Cmp(big.NewRat(3600, 1)) <= 0
}

func (s *Store) Revision(ctx context.Context) (int64, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT revision FROM meta WHERE id = 1`).Scan(&revision)
	return revision, err
}

func (s *Store) Test(ctx context.Context, id uuid.UUID) (contract.TestRecord, error) {
	var body []byte
	if err := s.db.QueryRowContext(ctx, `SELECT record FROM tests WHERE id = ?`, id.String()).Scan(&body); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return contract.TestRecord{}, ErrTestNotFound
		}
		return contract.TestRecord{}, err
	}
	var record contract.TestRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return contract.TestRecord{}, fmt.Errorf("invalid stored test: %w", err)
	}
	if record.TestId != id {
		return contract.TestRecord{}, errors.New("invalid stored test identity")
	}
	return record, nil
}

func (s *Store) Audit(ctx context.Context, id uuid.UUID, after int64, limit int) ([]contract.AuditEvent, error) {
	page, err := s.AuditPage(ctx, id, after, limit)
	return page.Items, err
}

func (s *Store) AuditPage(ctx context.Context, id uuid.UUID, after int64, limit int) (contract.AuditPage, error) {
	if after < 0 || limit < 1 || limit > 500 {
		return contract.AuditPage{}, errors.New("invalid audit page")
	}
	tx, err := s.beginTestRead(ctx, id)
	if err != nil {
		return contract.AuditPage{}, readError(ctx, err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT a.sequence, a.occurred_at, a.operator_id, a.action, a.outcome, a.revision, a.command_id, a.action_id, a.reason, t.instruction FROM audit a LEFT JOIN test_actions t ON t.action_id = a.action_id AND t.test_id = a.test_id WHERE a.test_id = ? AND a.sequence > ? ORDER BY a.sequence LIMIT ?`, id.String(), after, limit+1)
	if err != nil {
		return contract.AuditPage{}, readError(ctx, err)
	}
	defer func() { _ = rows.Close() }()
	events := make([]contract.AuditEvent, 0)
	for rows.Next() {
		var event contract.AuditEvent
		var when string
		var commandID, actionID, reason, instruction sql.NullString
		if err := rows.Scan(&event.Sequence, &when, &event.OperatorId, &event.Action, &event.Outcome, &event.Revision, &commandID, &actionID, &reason, &instruction); err != nil {
			return contract.AuditPage{}, readError(ctx, err)
		}
		event.TestId = id
		event.OccurredAt, err = time.Parse(time.RFC3339Nano, when)
		if err != nil {
			return contract.AuditPage{}, readError(ctx, err)
		}
		if commandID.Valid {
			parsed, err := uuid.Parse(commandID.String)
			if err != nil {
				return contract.AuditPage{}, readError(ctx, err)
			}
			event.CommandId = &parsed
		}
		if actionID.Valid {
			parsed, err := uuid.Parse(actionID.String)
			if err != nil {
				return contract.AuditPage{}, readError(ctx, err)
			}
			event.ActionId = &parsed
		}
		if reason.Valid {
			event.Reason = &reason.String
		}
		if instruction.Valid {
			event.Instruction = &instruction.String
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return contract.AuditPage{}, readError(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return contract.AuditPage{}, readError(ctx, err)
	}
	if err := tx.Commit(); err != nil {
		return contract.AuditPage{}, readError(ctx, err)
	}
	page := contract.AuditPage{Items: events}
	if len(events) > limit {
		page.Items = events[:limit]
		next := page.Items[len(page.Items)-1].Sequence
		page.NextAfter = &next
	}
	return page, nil
}
