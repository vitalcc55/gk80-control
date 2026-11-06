package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

// ListTests returns a bounded page in stable UUID order; it is not a chronological sort.
func (s *Store) ListTests(ctx context.Context, params contract.ListTestsParams) ([]contract.TestRecord, *uuid.UUID, *int64, error) {
	params, err := contract.NormalizeTestListParams(params)
	if err != nil {
		return nil, nil, nil, ErrInvalidCommand
	}
	limit := *params.Limit
	var createdFrom, createdBefore time.Time
	if params.CreatedFrom != nil {
		createdFrom, err = contract.ParseHistoryTime(*params.CreatedFrom)
		if err != nil {
			return nil, nil, nil, ErrInvalidCommand
		}
	}
	if params.CreatedBefore != nil {
		createdBefore, err = contract.ParseHistoryTime(*params.CreatedBefore)
		if err != nil {
			return nil, nil, nil, ErrInvalidCommand
		}
	}
	after := ""
	if params.After != nil {
		after = params.After.String()
	}
	// Record changes and revision are committed together; a changed revision means
	// the count from these independent chunks is not a single confirmed total.
	startRevision, err := s.Revision(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	cursor := ""
	var total int64
	query := ""
	if params.Q != nil {
		query = foldHistoryText(*params.Q)
	}
	items := make([]contract.TestRecord, 0)
	// Decode bounded portions for Unicode matching and exact timestamp comparisons.
	// Close each cursor before scanning again so history does not retain the single connection.
	const chunkSize = 128
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		rows, err := s.db.QueryContext(ctx, `SELECT id, record FROM tests WHERE id > ? ORDER BY id LIMIT ?`, cursor, chunkSize)
		if err != nil {
			return nil, nil, nil, readError(ctx, err)
		}
		count := 0
		err = func() error {
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var id string
				var body []byte
				if err := rows.Scan(&id, &body); err != nil {
					return err
				}
				var record contract.TestRecord
				if err := json.Unmarshal(body, &record); err != nil || record.TestId.String() != id {
					return errors.New("invalid stored test")
				}
				cursor = id
				count++
				if query != "" && !strings.Contains(foldHistoryText(id), query) && !strings.Contains(foldHistoryText(record.Parameters.Valve), query) {
					continue
				}
				if params.CreatedFrom != nil && record.CreatedAt.Before(createdFrom) {
					continue
				}
				if params.CreatedBefore != nil && !record.CreatedAt.Before(createdBefore) {
					continue
				}
				if params.Outcome != nil && record.Outcome != *params.Outcome {
					continue
				}
				total++
				if id > after && len(items) <= limit {
					items = append(items, record)
				}
			}
			return rows.Err()
		}()
		if err != nil {
			return nil, nil, nil, readError(ctx, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		if count < chunkSize {
			break
		}
	}
	endRevision, err := s.Revision(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	var stableTotal *int64
	if startRevision == endRevision {
		stableTotal = &total
	}
	if len(items) <= limit {
		return items, nil, stableTotal, nil
	}
	items = items[:limit]
	next := items[len(items)-1].TestId
	return items, &next, stableTotal, nil
}

// SimpleFold also unifies case variants such as Greek final sigma; lowercase alone does not.
func foldHistoryText(value string) string {
	return strings.Map(func(r rune) rune {
		canonical := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			canonical = min(canonical, next)
		}
		return canonical
	}, value)
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
