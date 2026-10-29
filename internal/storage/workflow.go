package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

// Start records the operator's confirmations and the pending demo action atomically.
func (s *Store) Start(ctx context.Context, testID uuid.UUID, command contract.StartTestCommand, action contract.PendingAction) (contract.CommandStatus, error) {
	request, err := startRequest(testID, command)
	if err != nil {
		return contract.CommandStatus{}, ErrInvalidCommand
	}
	return s.operatorCommand(ctx, command.CommandId, testID, nil, "start", request, func(tx *sql.Tx, wrote *bool) error {
		if command.CommandId == uuid.Nil || testID == uuid.Nil || command.ExpectedRevision < 0 || !command.ValveClosed || !command.ConnectionConfirmed || action.TestId != testID || action.ActionId == uuid.Nil || action.Instruction == "" {
			return ErrInvalidCommand
		}
		record, revision, err := currentRecord(ctx, tx, testID)
		if err != nil {
			return err
		}
		if revision != command.ExpectedRevision {
			return ErrRevision
		}
		if record.Stage != "preparing" {
			return ErrInvalidState
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		*wrote = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO test_actions(test_id, action_id, instruction, state) VALUES(?, ?, ?, 'pending')`, testID.String(), action.ActionId.String(), action.Instruction); err != nil {
			return err
		}
		record.Stage = "running"
		return saveTransition(ctx, tx, record, revision+1, operatorID, "start", "accepted", &command.CommandId, &action.ActionId)
	})
}

func startRequest(testID uuid.UUID, command contract.StartTestCommand) ([]byte, error) {
	return json.Marshal(struct {
		TestID  uuid.UUID                 `json:"testId"`
		Command contract.StartTestCommand `json:"command"`
	}{testID, command})
}

// LookupStart reads a previous command without creating or executing a new one.
func (s *Store) LookupStart(ctx context.Context, testID uuid.UUID, command contract.StartTestCommand) (contract.CommandStatus, error) {
	request, err := startRequest(testID, command)
	if err != nil {
		return contract.CommandStatus{}, ErrInvalidCommand
	}
	status, err := s.lookup(ctx, command.CommandId, "start", request)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.CommandStatus{}, ErrCommandNotFound
	}
	return status, err
}

// RejectStartNotReady persists the decision without applying a start transition.
func (s *Store) RejectStartNotReady(ctx context.Context, testID uuid.UUID, command contract.StartTestCommand) (contract.CommandStatus, error) {
	request, err := startRequest(testID, command)
	if err != nil {
		return contract.CommandStatus{}, ErrInvalidCommand
	}
	return s.operatorCommand(ctx, command.CommandId, testID, nil, "start", request, func(tx *sql.Tx, _ *bool) error {
		if command.CommandId == uuid.Nil || testID == uuid.Nil || command.ExpectedRevision < 0 || !command.ValveClosed || !command.ConnectionConfirmed {
			return ErrInvalidCommand
		}
		record, revision, err := currentRecord(ctx, tx, testID)
		if err != nil {
			return err
		}
		if revision != command.ExpectedRevision {
			return ErrRevision
		}
		if record.Stage != "preparing" {
			return ErrInvalidState
		}
		return ErrNotReady
	})
}

// RejectStartUnavailable preserves the command identity without starting a non-current test.
func (s *Store) RejectStartUnavailable(ctx context.Context, testID uuid.UUID, command contract.StartTestCommand) (contract.CommandStatus, error) {
	request, err := startRequest(testID, command)
	if err != nil {
		return contract.CommandStatus{}, ErrInvalidCommand
	}
	return s.operatorCommand(ctx, command.CommandId, testID, nil, "start", request, func(tx *sql.Tx, _ *bool) error {
		if testID == uuid.Nil || command.ExpectedRevision < 0 || !command.ValveClosed || !command.ConnectionConfirmed {
			return ErrInvalidCommand
		}
		if _, _, err := currentRecord(ctx, tx, testID); err != nil {
			return err
		}
		return ErrInvalidState
	})
}

// Confirm records confirmation of the current action, scoped to its test and action IDs.
func (s *Store) Confirm(ctx context.Context, testID, actionID uuid.UUID, command contract.ConfirmActionCommand) (contract.CommandStatus, error) {
	request, err := confirmRequest(testID, actionID, command)
	if err != nil {
		return contract.CommandStatus{}, ErrInvalidCommand
	}
	return s.operatorCommand(ctx, command.CommandId, testID, &actionID, "confirm", request, func(tx *sql.Tx, wrote *bool) error {
		if command.CommandId == uuid.Nil || testID == uuid.Nil || actionID == uuid.Nil || command.ExpectedRevision < 0 {
			return ErrInvalidCommand
		}
		record, revision, err := currentRecord(ctx, tx, testID)
		if err != nil {
			return err
		}
		if revision != command.ExpectedRevision {
			return ErrRevision
		}
		if record.Stage != "running" {
			return ErrInvalidState
		}
		var pending string
		err = tx.QueryRowContext(ctx, `SELECT action_id FROM test_actions WHERE test_id = ? AND state = 'pending'`, testID.String()).Scan(&pending)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && pending != actionID.String()) {
			return ErrActionMismatch
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		*wrote = true
		if _, err := tx.ExecContext(ctx, `UPDATE test_actions SET state = 'confirmed' WHERE test_id = ? AND state = 'pending'`, testID.String()); err != nil {
			return err
		}
		return saveTransition(ctx, tx, record, revision+1, operatorID, "confirm", "completed", &command.CommandId, &actionID)
	})
}

func confirmRequest(testID, actionID uuid.UUID, command contract.ConfirmActionCommand) ([]byte, error) {
	return json.Marshal(struct {
		TestID   uuid.UUID                     `json:"testId"`
		ActionID uuid.UUID                     `json:"actionId"`
		Command  contract.ConfirmActionCommand `json:"command"`
	}{testID, actionID, command})
}

// LookupConfirm reads a previous confirmation without changing the test.
func (s *Store) LookupConfirm(ctx context.Context, testID, actionID uuid.UUID, command contract.ConfirmActionCommand) (contract.CommandStatus, error) {
	request, err := confirmRequest(testID, actionID, command)
	if err != nil {
		return contract.CommandStatus{}, ErrInvalidCommand
	}
	status, err := s.lookup(ctx, command.CommandId, "confirm", request)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.CommandStatus{}, ErrCommandNotFound
	}
	return status, err
}

func (s *Store) RejectConfirmUnavailable(ctx context.Context, testID, actionID uuid.UUID, command contract.ConfirmActionCommand) (contract.CommandStatus, error) {
	request, err := confirmRequest(testID, actionID, command)
	if err != nil {
		return contract.CommandStatus{}, ErrInvalidCommand
	}
	return s.operatorCommand(ctx, command.CommandId, testID, &actionID, "confirm", request, func(tx *sql.Tx, _ *bool) error {
		if testID == uuid.Nil || actionID == uuid.Nil || command.ExpectedRevision < 0 {
			return ErrInvalidCommand
		}
		if _, _, err := currentRecord(ctx, tx, testID); err != nil {
			return err
		}
		return ErrInvalidState
	})
}

func (s *Store) operatorCommand(ctx context.Context, id, testID uuid.UUID, actionID *uuid.UUID, operation string, request []byte, apply func(*sql.Tx, *bool) error) (contract.CommandStatus, error) {
	if id == uuid.Nil {
		return contract.CommandStatus{}, ErrInvalidCommand
	}
	if err := s.lockWrite(ctx); err != nil {
		return contract.CommandStatus{}, errors.Join(ErrWriteNotStarted, err)
	}
	defer s.unlockWrite()
	if err := ctx.Err(); err != nil {
		return contract.CommandStatus{}, errors.Join(ErrWriteNotStarted, err)
	}
	status, err := s.lookup(ctx, id, operation, request)
	if !errors.Is(err, sql.ErrNoRows) {
		if err != nil && !expectedCommandError(err) && ctx.Err() == nil {
			s.failed = true
		}
		return status, beforeCommandWrite(ctx, err)
	}
	if s.failed {
		return contract.CommandStatus{}, ErrStorageUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		if ctx.Err() == nil {
			s.failed = true
		}
		return contract.CommandStatus{}, beforeCommandWrite(ctx, err)
	}
	defer func() { _ = tx.Rollback() }()
	wrote := false
	err = apply(tx, &wrote)
	if expectedCommandError(err) {
		if wrote {
			s.failed = true
			return contract.CommandStatus{}, errors.New("partial command transition")
		}
		reason := commandReason(err)
		if contextErr := ctx.Err(); contextErr != nil {
			s.noteWriteError(ctx, contextErr, false)
			return contract.CommandStatus{}, beforeCommandWrite(ctx, contextErr)
		}
		var exists int
		if queryErr := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tests WHERE id = ?)`, testID.String()).Scan(&exists); queryErr != nil {
			s.noteWriteError(ctx, queryErr, false)
			return contract.CommandStatus{}, beforeCommandWrite(ctx, queryErr)
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return contract.CommandStatus{}, beforeCommandWrite(ctx, contextErr)
		}
		var linked sql.NullString
		if exists != 0 {
			linked = sql.NullString{String: testID.String(), Valid: true}
		}
		wrote = true
		if _, insertErr := tx.ExecContext(ctx, `INSERT INTO commands(id, operation, request, status, test_id, reason, operator_id, occurred_at) VALUES(?, ?, ?, 'rejected', ?, ?, ?, ?)`, id.String(), operation, request, linked, reason, operatorID, time.Now().UTC().Format(time.RFC3339Nano)); insertErr != nil {
			s.noteWriteError(ctx, insertErr, false)
			return contract.CommandStatus{}, insertErr
		}
		if exists != 0 {
			var revision int64
			if queryErr := tx.QueryRowContext(ctx, `SELECT revision FROM meta WHERE id = 1`).Scan(&revision); queryErr != nil {
				s.noteWriteError(ctx, queryErr, false)
				return contract.CommandStatus{}, queryErr
			}
			var action sql.NullString
			if actionID != nil {
				action = sql.NullString{String: actionID.String(), Valid: true}
			}
			if _, insertErr := tx.ExecContext(ctx, `INSERT INTO audit(test_id, occurred_at, operator_id, action, outcome, revision, command_id, action_id, reason) VALUES(?, ?, ?, ?, 'rejected', ?, ?, ?, ?)`, testID.String(), time.Now().UTC().Format(time.RFC3339Nano), operatorID, operation, revision, id.String(), action, reason); insertErr != nil {
				s.noteWriteError(ctx, insertErr, false)
				return contract.CommandStatus{}, insertErr
			}
		}
		if commitErr := tx.Commit(); commitErr != nil {
			s.noteWriteError(ctx, commitErr, false)
			return contract.CommandStatus{}, commitErr
		}
		result := contract.CommandStatus{CommandId: id, Status: "rejected", Reason: &reason}
		if exists != 0 {
			result.TestId = &testID
		}
		return result, err
	}
	if err != nil {
		s.noteWriteError(ctx, err, false)
		if !wrote {
			err = beforeCommandWrite(ctx, err)
		}
		return contract.CommandStatus{}, err
	}
	if err := ctx.Err(); err != nil {
		s.noteWriteError(ctx, err, false)
		if !wrote {
			err = beforeCommandWrite(ctx, err)
		}
		return contract.CommandStatus{}, err
	}
	wrote = true
	commandStatus := contract.CommandStatusStatusCompleted
	if operation == "start" {
		commandStatus = contract.CommandStatusStatusAccepted
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO commands(id, operation, request, status, test_id, operator_id, occurred_at) VALUES(?, ?, ?, ?, ?, ?, ?)`, id.String(), operation, request, commandStatus, testID.String(), operatorID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		s.noteWriteError(ctx, err, false)
		return contract.CommandStatus{}, err
	}
	if err := tx.Commit(); err != nil {
		s.noteWriteError(ctx, err, false)
		return contract.CommandStatus{}, err
	}
	return contract.CommandStatus{CommandId: id, Status: commandStatus, TestId: &testID}, nil
}

// beforeCommandWrite marks only cancellations known to precede any write attempt.
func beforeCommandWrite(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil && (errors.Is(err, ctx.Err()) || errors.Is(err, sql.ErrTxDone)) {
		return errors.Join(ErrWriteNotStarted, ctx.Err(), err)
	}
	return err
}

func expectedCommandError(err error) bool {
	return errors.Is(err, ErrCommandConflict) || errors.Is(err, ErrInvalidCommand) || errors.Is(err, ErrRevision) || errors.Is(err, ErrInvalidState) || errors.Is(err, ErrActionMismatch) || errors.Is(err, ErrTestNotFound) || errors.Is(err, ErrNotReady)
}
func commandReason(err error) string {
	switch {
	case errors.Is(err, ErrInvalidCommand):
		return "invalid_command"
	case errors.Is(err, ErrRevision):
		return "revision"
	case errors.Is(err, ErrInvalidState):
		return "invalid_state"
	case errors.Is(err, ErrActionMismatch):
		return "action_mismatch"
	case errors.Is(err, ErrTestNotFound):
		return "test_not_found"
	case errors.Is(err, ErrNotReady):
		return "not_ready"
	default:
		return "invalid_command"
	}
}

func currentRecord(ctx context.Context, tx *sql.Tx, id uuid.UUID) (contract.TestRecord, int64, error) {
	var body []byte
	if err := tx.QueryRowContext(ctx, `SELECT record FROM tests WHERE id = ?`, id.String()).Scan(&body); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return contract.TestRecord{}, 0, ErrTestNotFound
		}
		return contract.TestRecord{}, 0, err
	}
	var record contract.TestRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return record, 0, err
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM meta WHERE id = 1`).Scan(&revision); err != nil {
		return record, 0, err
	}
	return record, revision, nil
}

func saveTransition(ctx context.Context, tx *sql.Tx, record contract.TestRecord, revision int64, actor, action, outcome string, commandID, actionID *uuid.UUID) error {
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tests SET record = ?, stage = ? WHERE id = ?`, body, record.Stage, record.TestId.String()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meta SET revision = ? WHERE id = 1`, revision); err != nil {
		return err
	}
	var command, actionValue sql.NullString
	if commandID != nil {
		command = sql.NullString{String: commandID.String(), Valid: true}
	}
	if actionID != nil {
		actionValue = sql.NullString{String: actionID.String(), Valid: true}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit(test_id, occurred_at, operator_id, action, outcome, revision, command_id, action_id) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, record.TestId.String(), time.Now().UTC().Format(time.RFC3339Nano), actor, action, outcome, revision, command, actionValue)
	return err
}

func (s *Store) PendingAction(ctx context.Context, testID uuid.UUID) (*contract.PendingAction, error) {
	var id, instruction string
	err := s.db.QueryRowContext(ctx, `SELECT action_id, instruction FROM test_actions WHERE test_id = ? AND state = 'pending'`, testID.String()).Scan(&id, &instruction)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil, err
	}
	return &contract.PendingAction{TestId: testID, ActionId: parsed, Instruction: instruction}, nil
}

func validMeasurement(m contract.Measurement) bool {
	if m.Sequence < 1 || !m.Channel.Valid() || !m.Quantity.Valid() || !m.Quality.Valid() || m.Unit != "Pa" || m.Source != "simulator" || m.ObservedAt.IsZero() {
		return false
	}
	if (m.Channel == "dd1") != (m.Quantity == "pressure") {
		return false
	}
	if m.Quality == "unavailable" {
		return m.Value == nil && m.MeasuredAt == nil
	}
	if m.Value == nil || m.MeasuredAt == nil || math.IsInf(*m.Value, 0) || math.IsNaN(*m.Value) {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, *m.MeasuredAt)
	return err == nil
}

// TimedMeasurement carries the engine's monotonic offset alongside absolute history times.
type TimedMeasurement struct {
	Measurement contract.Measurement
	Elapsed     time.Duration
}

// RecordMeasurements commits a bounded, contiguous batch of original simulator samples.
func (s *Store) RecordMeasurements(ctx context.Context, testID uuid.UUID, measurements []TimedMeasurement) error {
	if len(measurements) == 0 || len(measurements) > 128 {
		return ErrInvalidCommand
	}
	bodies := make([][]byte, len(measurements))
	for i, sample := range measurements {
		if !validMeasurement(sample.Measurement) || sample.Elapsed < 0 || sample.Elapsed > 24*time.Hour {
			return ErrInvalidCommand
		}
		body, err := json.Marshal(sample.Measurement)
		if err != nil {
			return err
		}
		bodies[i] = body
	}
	if err := s.lockWrite(ctx); err != nil {
		return err
	}
	defer s.unlockWrite()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.failed {
		return ErrStorageUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		if ctx.Err() == nil {
			s.failed = true
		}
		return err
	}
	defer func() { _ = tx.Rollback() }()
	record, _, err := currentRecord(ctx, tx, testID)
	if err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	if record.Stage != "running" {
		return ErrInvalidState
	}
	var pending, incompleteStart int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM test_actions WHERE test_id = ? AND state = 'pending'`, testID.String()).Scan(&pending); err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM commands WHERE test_id = ? AND operation = 'start' AND status = 'accepted'`, testID.String()).Scan(&incompleteStart); err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	if pending != 0 || incompleteStart != 0 {
		return ErrInvalidState
	}
	var last, lastElapsed int64
	err = tx.QueryRowContext(ctx, `SELECT sequence, elapsed_ns FROM measurements WHERE test_id = ? ORDER BY sequence DESC LIMIT 1`, testID.String()).Scan(&last, &lastElapsed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.noteWriteError(ctx, err, false)
		return err
	}
	for i, sample := range measurements {
		if sample.Measurement.Sequence != last+int64(i)+1 || int64(sample.Elapsed) < lastElapsed {
			return ErrInvalidCommand
		}
		lastElapsed = int64(sample.Elapsed)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for i, sample := range measurements {
		if _, err := tx.ExecContext(ctx, `INSERT INTO measurements(test_id, sequence, elapsed_ns, channel, quality, payload) VALUES(?, ?, ?, ?, ?, ?)`, testID.String(), sample.Measurement.Sequence, int64(sample.Elapsed), sample.Measurement.Channel, sample.Measurement.Quality, bodies[i]); err != nil {
			s.noteWriteError(ctx, err, true)
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		s.noteWriteError(ctx, err, true)
		return err
	}
	return nil
}

func (s *Store) Measurements(ctx context.Context, testID uuid.UUID, after int64, limit int) ([]contract.Measurement, error) {
	page, err := s.MeasurementsPage(ctx, testID, after, limit)
	return page.Items, err
}

func (s *Store) MeasurementsPage(ctx context.Context, testID uuid.UUID, after int64, limit int) (contract.MeasurementPage, error) {
	if after < 0 || limit < 1 || limit > 500 {
		return contract.MeasurementPage{}, errors.New("invalid measurement page")
	}
	tx, err := s.beginTestRead(ctx, testID)
	if err != nil {
		return contract.MeasurementPage{}, readError(ctx, err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT sequence, payload FROM measurements WHERE test_id = ? AND sequence > ? ORDER BY sequence LIMIT ?`, testID.String(), after, limit+1)
	if err != nil {
		return contract.MeasurementPage{}, readError(ctx, err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]contract.Measurement, 0)
	for rows.Next() {
		var sequence int64
		var body []byte
		if err := rows.Scan(&sequence, &body); err != nil {
			return contract.MeasurementPage{}, readError(ctx, err)
		}
		var measurement contract.Measurement
		if err := json.Unmarshal(body, &measurement); err != nil || !validMeasurement(measurement) || measurement.Sequence != sequence {
			return contract.MeasurementPage{}, errors.New("invalid stored measurement")
		}
		result = append(result, measurement)
	}
	if err := rows.Err(); err != nil {
		return contract.MeasurementPage{}, readError(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return contract.MeasurementPage{}, readError(ctx, err)
	}
	if err := tx.Commit(); err != nil {
		return contract.MeasurementPage{}, readError(ctx, err)
	}
	page := contract.MeasurementPage{Items: result}
	if len(result) > limit {
		page.Items = result[:limit]
		next := page.Items[len(page.Items)-1].Sequence
		page.NextAfter = &next
	}
	return page, nil
}

// Complete receives the elapsed interval measured by the engine's monotonic clock.
func (s *Store) Complete(ctx context.Context, testID uuid.UUID, elapsed time.Duration) error {
	if elapsed < 0 || elapsed > 24*time.Hour {
		return ErrInvalidCommand
	}
	if err := s.lockWrite(ctx); err != nil {
		return err
	}
	defer s.unlockWrite()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.failed {
		return ErrStorageUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		if ctx.Err() == nil {
			s.failed = true
		}
		return err
	}
	defer func() { _ = tx.Rollback() }()
	record, revision, err := currentRecord(ctx, tx, testID)
	if err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	if record.Stage != "running" {
		return ErrInvalidState
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM test_actions WHERE test_id = ? AND state = 'pending'`, testID.String()).Scan(&pending); err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	if pending != 0 {
		return ErrInvalidState
	}
	rows, err := tx.QueryContext(ctx, `SELECT payload, elapsed_ns FROM measurements WHERE test_id = ? AND channel = 'dd1' ORDER BY sequence`, testID.String())
	if err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	var first, last int64
	count := 0
	for rows.Next() {
		var body []byte
		var offset int64
		if err := rows.Scan(&body, &offset); err != nil {
			_ = rows.Close()
			s.noteWriteError(ctx, err, false)
			return err
		}
		var sample contract.Measurement
		if err := json.Unmarshal(body, &sample); err != nil || !validMeasurement(sample) {
			_ = rows.Close()
			s.failed = true
			return errors.New("invalid stored measurement")
		}
		if sample.Quality != "valid" || sample.MeasuredAt == nil {
			_ = rows.Close()
			return ErrInvalidState
		}
		if count == 0 {
			first = offset
		} else if offset < last {
			_ = rows.Close()
			return ErrInvalidState
		}
		last = offset
		count++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		s.noteWriteError(ctx, err, false)
		return err
	}
	if err := rows.Close(); err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	if count < 2 || last > int64(elapsed) || float64(last-first)/float64(time.Second) < record.Parameters.MeasurementSeconds || elapsed.Seconds() < record.Parameters.MeasurementSeconds {
		return ErrInvalidState
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now().UTC()
	record.Stage = "finished"
	record.Outcome = "completed"
	record.DataQuality = "complete"
	record.FinishedAt = &now
	if err := saveTransition(ctx, tx, record, revision+1, "system", "complete", "completed", nil, nil); err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tests SET measurement_elapsed_ns = ? WHERE id = ?`, int64(elapsed), testID.String()); err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	if err := tx.Commit(); err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	return nil
}

// Recover records that unfinished procedures have an unknown outcome; it never runs them.
func (s *Store) Recover(ctx context.Context) error {
	if err := s.lockWrite(ctx); err != nil {
		return err
	}
	defer s.unlockWrite()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.failed {
		return ErrStorageUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		if ctx.Err() == nil {
			s.failed = true
		}
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT record FROM tests WHERE stage != 'finished'`)
	if err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	records := make([]contract.TestRecord, 0)
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			_ = rows.Close()
			s.noteWriteError(ctx, err, false)
			return err
		}
		var record contract.TestRecord
		if err := json.Unmarshal(body, &record); err != nil {
			_ = rows.Close()
			s.noteWriteError(ctx, err, false)
			return err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		s.noteWriteError(ctx, err, false)
		return err
	}
	if err := rows.Close(); err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	if len(records) == 0 {
		return nil
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM meta WHERE id = 1`).Scan(&revision); err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, record := range records {
		record.Stage = "finished"
		record.Outcome = "unknown"
		record.DataQuality = "incomplete"
		record.FinishedAt = nil
		if _, err := tx.ExecContext(ctx, `UPDATE test_actions SET state = 'unknown' WHERE test_id = ? AND state = 'pending'`, record.TestId.String()); err != nil {
			s.noteWriteError(ctx, err, true)
			return err
		}
		if err := markAcceptedUnknown(ctx, tx, record.TestId, &revision); err != nil {
			s.noteWriteError(ctx, err, true)
			return err
		}
		revision++
		if err := saveTransition(ctx, tx, record, revision, "system", "recovered", "unknown", nil, nil); err != nil {
			s.noteWriteError(ctx, err, true)
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		s.noteWriteError(ctx, err, true)
		return err
	}
	return nil
}

// CompleteStart confirms that the simulator has accepted the starting action.
func (s *Store) CompleteStart(ctx context.Context, testID, commandID uuid.UUID) error {
	if err := s.lockWrite(ctx); err != nil {
		return err
	}
	defer s.unlockWrite()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.failed {
		return ErrStorageUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		if ctx.Err() == nil {
			s.failed = true
		}
		return err
	}
	defer func() { _ = tx.Rollback() }()
	record, revision, err := currentRecord(ctx, tx, testID)
	if err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	var operation, state string
	var linked sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT operation, status, test_id FROM commands WHERE id = ?`, commandID.String()).Scan(&operation, &state, &linked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCommandConflict
		}
		s.noteWriteError(ctx, err, false)
		return err
	}
	if operation != "start" || !linked.Valid || linked.String != testID.String() {
		return ErrCommandConflict
	}
	if state == "completed" {
		return nil
	}
	if state != "accepted" || record.Stage != "running" {
		return ErrInvalidState
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM test_actions WHERE test_id = ? AND state = 'pending'`, testID.String()).Scan(&pending); err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	if pending != 0 {
		return ErrInvalidState
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE commands SET status = 'completed' WHERE id = ?`, commandID.String()); err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	if err := saveTransition(ctx, tx, record, revision+1, "system", "start", "completed", &commandID, nil); err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	if err := tx.Commit(); err != nil {
		s.noteWriteError(ctx, err, false)
		return err
	}
	return nil
}

// LookupStop never invokes the simulator. A reserved command has no durable outcome.
func (s *Store) LookupStop(ctx context.Context, testID, commandID uuid.UUID) (contract.CommandStatus, error) {
	var operation, state string
	var linked sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT operation, status, test_id FROM commands WHERE id = ?`, commandID.String()).Scan(&operation, &state, &linked)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (operation != "stop" || !linked.Valid || linked.String != testID.String())) {
		return contract.CommandStatus{}, ErrCommandConflict
	}
	if err != nil {
		return contract.CommandStatus{}, err
	}
	if state == "reserved" {
		state = "unknown"
	}
	return contract.CommandStatus{CommandId: commandID, TestId: &testID, Status: contract.CommandStatusStatus(state)}, nil
}

type StopDetails struct {
	Actor            string
	Reason           string
	AcceptedAt       time.Time
	Failed           bool
	MeasurementsLost bool
}

// FinishStop persists a simulator-confirmed stop. The engine must invoke the port before this call.
func (s *Store) FinishStop(ctx context.Context, testID, commandID uuid.UUID, details StopDetails) (contract.CommandStatus, error) {
	if testID == uuid.Nil || commandID == uuid.Nil || details.AcceptedAt.IsZero() || (details.Actor != operatorID && details.Actor != "system") || details.Reason == "" {
		return contract.CommandStatus{}, ErrInvalidCommand
	}
	if err := s.lockWrite(ctx); err != nil {
		return contract.CommandStatus{}, err
	}
	defer s.unlockWrite()
	if err := ctx.Err(); err != nil {
		return contract.CommandStatus{}, err
	}
	var operation, state string
	var linked sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT operation, status, test_id FROM commands WHERE id = ?`, commandID.String()).Scan(&operation, &state, &linked)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.CommandStatus{}, ErrCommandConflict
	}
	if err != nil {
		if ctx.Err() == nil {
			s.failed = true
		}
		return contract.CommandStatus{}, err
	}
	if operation != "stop" || !linked.Valid || linked.String != testID.String() {
		return contract.CommandStatus{}, ErrCommandConflict
	}
	if state == "completed" || state == "failed" {
		return contract.CommandStatus{CommandId: commandID, Status: contract.CommandStatusStatus(state), TestId: &testID}, nil
	}
	if state != "reserved" {
		return contract.CommandStatus{}, ErrInvalidState
	}
	if s.failed {
		return contract.CommandStatus{}, ErrStorageUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		if ctx.Err() == nil {
			s.failed = true
		}
		return contract.CommandStatus{}, err
	}
	defer func() { _ = tx.Rollback() }()
	record, revision, err := currentRecord(ctx, tx, testID)
	if err != nil {
		s.noteWriteError(ctx, err, false)
		return contract.CommandStatus{}, err
	}
	if record.Stage == "finished" {
		return contract.CommandStatus{}, ErrInvalidState
	}
	if err := ctx.Err(); err != nil {
		return contract.CommandStatus{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE test_actions SET state = 'canceled' WHERE test_id = ? AND state = 'pending'`, testID.String()); err != nil {
		s.noteWriteError(ctx, err, false)
		return contract.CommandStatus{}, err
	}
	record.Stage = "stopping"
	if err := saveTransition(ctx, tx, record, revision+1, details.Actor, "stop", "accepted", &commandID, nil); err != nil {
		s.noteWriteError(ctx, err, false)
		return contract.CommandStatus{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE audit SET occurred_at = ?, reason = ? WHERE sequence = last_insert_rowid()`, details.AcceptedAt.UTC().Format(time.RFC3339Nano), details.Reason); err != nil {
		s.noteWriteError(ctx, err, false)
		return contract.CommandStatus{}, err
	}
	revision++
	if err := markAcceptedUnknown(ctx, tx, testID, &revision); err != nil {
		s.noteWriteError(ctx, err, false)
		return contract.CommandStatus{}, err
	}
	status := "completed"
	if details.Failed {
		status = "failed"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE commands SET status = ? WHERE id = ?`, status, commandID.String()); err != nil {
		s.noteWriteError(ctx, err, false)
		return contract.CommandStatus{}, err
	}
	now := time.Now().UTC()
	record.Stage = "finished"
	record.Outcome = "interrupted"
	record.DataQuality = "incomplete"
	if details.MeasurementsLost {
		record.SaveStatus = "failed"
	}
	record.FinishedAt = &now
	if details.Failed {
		record.Stage = "stopping"
		record.Outcome = "unknown"
		record.FinishedAt = nil
	}
	if err := saveTransition(ctx, tx, record, revision+1, details.Actor, "stop", status, &commandID, nil); err != nil {
		s.noteWriteError(ctx, err, false)
		return contract.CommandStatus{}, err
	}
	if err := tx.Commit(); err != nil {
		s.noteWriteError(ctx, err, false)
		return contract.CommandStatus{}, err
	}
	return contract.CommandStatus{CommandId: commandID, Status: contract.CommandStatusStatus(status), TestId: &testID}, nil
}

func markAcceptedUnknown(ctx context.Context, tx *sql.Tx, testID uuid.UUID, revision *int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, operation FROM commands WHERE test_id = ? AND status = 'accepted' ORDER BY id`, testID.String())
	if err != nil {
		return err
	}
	type accepted struct{ id, operation string }
	commands := make([]accepted, 0)
	for rows.Next() {
		var command accepted
		if err := rows.Scan(&command.id, &command.operation); err != nil {
			_ = rows.Close()
			return err
		}
		commands = append(commands, command)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, command := range commands {
		if _, err := tx.ExecContext(ctx, `UPDATE commands SET status = 'unknown' WHERE id = ?`, command.id); err != nil {
			return err
		}
		*revision++
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit(test_id, occurred_at, operator_id, action, outcome, revision, command_id) VALUES(?, ?, 'system', ?, 'unknown', ?, ?)`, testID.String(), time.Now().UTC().Format(time.RFC3339Nano), command.operation, *revision, command.id); err != nil {
			return err
		}
	}
	return nil
}
