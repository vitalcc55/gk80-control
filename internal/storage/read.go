package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

// ListTests returns a bounded page in stable UUID order; it is not a chronological sort.
func (s *Store) ListTests(ctx context.Context, after *uuid.UUID, limit int) ([]contract.TestRecord, *uuid.UUID, error) {
	if limit < 1 || limit > 500 || (after != nil && *after == uuid.Nil) {
		return nil, nil, ErrInvalidCommand
	}
	cursor := ""
	if after != nil {
		cursor = after.String()
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, record FROM tests WHERE id > ? ORDER BY id LIMIT ?`, cursor, limit+1)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]contract.TestRecord, 0)
	for rows.Next() {
		var id string
		var body []byte
		if err := rows.Scan(&id, &body); err != nil {
			return nil, nil, err
		}
		var record contract.TestRecord
		if err := json.Unmarshal(body, &record); err != nil || record.TestId.String() != id {
			return nil, nil, errors.New("invalid stored test")
		}
		items = append(items, record)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(items) <= limit {
		return items, nil, nil
	}
	items = items[:limit]
	next := items[len(items)-1].TestId
	return items, &next, nil
}

// Command reads the stored result without replaying its original request.
func (s *Store) Command(ctx context.Context, id uuid.UUID) (contract.CommandStatus, error) {
	if id == uuid.Nil {
		return contract.CommandStatus{}, ErrInvalidCommand
	}
	var operation, state string
	var testID, reason sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT operation, status, test_id, reason FROM commands WHERE id = ?`, id.String()).Scan(&operation, &state, &testID, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.CommandStatus{}, ErrCommandNotFound
	}
	if err != nil {
		return contract.CommandStatus{}, err
	}
	if state == "reserved" && operation == "stop" {
		state = "unknown"
	}
	status := contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatus(state)}
	if !status.Status.Valid() {
		return contract.CommandStatus{}, errors.New("invalid stored command status")
	}
	if testID.Valid {
		parsed, err := uuid.Parse(testID.String)
		if err != nil {
			return contract.CommandStatus{}, err
		}
		status.TestId = &parsed
	}
	if reason.Valid {
		status.Reason = &reason.String
	}
	return status, nil
}

func (s *Store) beginTestRead(ctx context.Context, id uuid.UUID) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, readError(ctx, err)
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM tests WHERE id = ?`, id.String()).Scan(&exists); err != nil {
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTestNotFound
		}
		return nil, readError(ctx, err)
	}
	return tx, nil
}

func readError(ctx context.Context, err error) error {
	if errors.Is(err, sql.ErrTxDone) && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
