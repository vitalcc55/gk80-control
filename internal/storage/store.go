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
)

const operatorID = "demo-operator"

// Store is opened only while the core owns the corresponding profile lock.
type Store struct {
	db      *sql.DB
	writeMu sync.Mutex
	failed  bool
}

func Open(profileDir string) (*Store, error) {
	if !filepath.IsAbs(profileDir) {
		return nil, errors.New("profile directory must be absolute")
	}
	db, err := sql.Open("sqlite", filepath.Join(profileDir, "demo.sqlite"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) initialize() error {
	if _, err := s.db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return err
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version == 1 {
		return nil
	}
	if version != 0 {
		return errors.New("unsupported database schema")
	}
	var objects int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&objects); err != nil {
		return err
	}
	if objects != 0 {
		return errors.New("unrecognized database")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	statements := []string{
		`CREATE TABLE meta (id INTEGER PRIMARY KEY CHECK (id = 1), revision INTEGER NOT NULL CHECK (revision >= 0))`,
		`INSERT INTO meta (id, revision) VALUES (1, 0)`,
		`CREATE TABLE tests (id TEXT PRIMARY KEY, record BLOB NOT NULL, stage TEXT NOT NULL)`,
		`CREATE TABLE commands (id TEXT PRIMARY KEY, operation TEXT NOT NULL, request BLOB NOT NULL, status TEXT NOT NULL, test_id TEXT REFERENCES tests(id), reason TEXT, operator_id TEXT NOT NULL, occurred_at TEXT NOT NULL)`,
		`CREATE TABLE audit (sequence INTEGER PRIMARY KEY AUTOINCREMENT, test_id TEXT NOT NULL REFERENCES tests(id), occurred_at TEXT NOT NULL, operator_id TEXT NOT NULL, action TEXT NOT NULL, outcome TEXT NOT NULL, revision INTEGER NOT NULL, command_id TEXT, action_id TEXT, reason TEXT)`,
		`CREATE INDEX audit_by_test ON audit(test_id, sequence)`,
		`PRAGMA user_version = 1`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }

func valid(command contract.CreateTestCommand) bool {
	p := command.Parameters
	number := func(v float64) bool { return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v) }
	return command.CommandId != uuid.Nil && command.ExpectedRevision >= 0 && strings.TrimSpace(p.Valve) != "" && utf8.RuneCountInString(p.Valve) <= 200 && p.Direction == "pressure" && !p.Actuator && number(p.AreaM2) && number(p.PmaxPa) && number(p.MeasurementSeconds)
}

// Create commits the immutable parameters, command, reserved stop ID and audit together.
func (s *Store) Create(ctx context.Context, command contract.CreateTestCommand) (contract.CommandStatus, error) {
	if command.CommandId == uuid.Nil {
		return contract.CommandStatus{}, ErrInvalidCommand
	}
	request, marshalErr := json.Marshal(command)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return contract.CommandStatus{}, err
	}
	status, err := s.lookup(ctx, command.CommandId, request)
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
	s.noteWriteError(ctx, err, wrote)
	return status, err
}

func (s *Store) noteWriteError(ctx context.Context, err error, writeAttempted bool) {
	if err == nil || errors.Is(err, ErrRevision) || errors.Is(err, ErrActiveTest) || errors.Is(err, ErrInvalidCommand) {
		return
	}
	if !writeAttempted && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || (ctx.Err() != nil && errors.Is(err, sql.ErrTxDone))) {
		return
	}
	s.failed = true
}

func (s *Store) lookup(ctx context.Context, id uuid.UUID, request []byte) (contract.CommandStatus, error) {
	var operation, state string
	var testID, reason sql.NullString
	var stored []byte
	err := s.db.QueryRowContext(ctx, `SELECT operation, request, status, test_id, reason FROM commands WHERE id = ?`, id.String()).Scan(&operation, &stored, &state, &testID, &reason)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	if operation != "create" || !bytes.Equal(stored, request) {
		return contract.CommandStatus{}, ErrCommandConflict
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
		switch reason.String {
		case "revision":
			return status, ErrRevision
		case "active_test":
			return status, ErrActiveTest
		case "invalid_command":
			return status, ErrInvalidCommand
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
	if !valid(command) {
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

func (s *Store) Test(ctx context.Context, id uuid.UUID) (contract.TestRecord, error) {
	var body []byte
	if err := s.db.QueryRowContext(ctx, `SELECT record FROM tests WHERE id = ?`, id.String()).Scan(&body); err != nil {
		return contract.TestRecord{}, err
	}
	var record contract.TestRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return contract.TestRecord{}, fmt.Errorf("invalid stored test: %w", err)
	}
	return record, nil
}

func (s *Store) Audit(ctx context.Context, id uuid.UUID, after int64, limit int) ([]contract.AuditEvent, error) {
	if after < 0 || limit < 1 || limit > 500 {
		return nil, errors.New("invalid audit page")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, occurred_at, operator_id, action, outcome, revision, command_id, action_id, reason FROM audit WHERE test_id = ? AND sequence > ? ORDER BY sequence LIMIT ?`, id.String(), after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	events := make([]contract.AuditEvent, 0)
	for rows.Next() {
		var event contract.AuditEvent
		var when string
		var commandID, actionID, reason sql.NullString
		if err := rows.Scan(&event.Sequence, &when, &event.OperatorId, &event.Action, &event.Outcome, &event.Revision, &commandID, &actionID, &reason); err != nil {
			return nil, err
		}
		event.TestId = id
		event.OccurredAt, err = time.Parse(time.RFC3339Nano, when)
		if err != nil {
			return nil, err
		}
		if commandID.Valid {
			parsed, err := uuid.Parse(commandID.String)
			if err != nil {
				return nil, err
			}
			event.CommandId = &parsed
		}
		if actionID.Valid {
			parsed, err := uuid.Parse(actionID.String)
			if err != nil {
				return nil, err
			}
			event.ActionId = &parsed
		}
		if reason.Valid {
			event.Reason = &reason.String
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}
