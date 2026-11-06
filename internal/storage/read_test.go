package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

func TestHistoryFiltersSearchBeyondFirstPortionAndSurviveReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	anchor := time.Date(2026, 11, 1, 0, 0, 0, 123456789, time.FixedZone("local", 3*3600))
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 132; index++ {
		name := "Другой клапан"
		if index >= 128 {
			name = "КЛАПАН 100%_А ΟΣ"
		}
		outcome := contract.TestRecordOutcome("completed")
		if index == 130 {
			outcome = "interrupted"
		}
		record := contract.TestRecord{TestId: uuid.MustParse(fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1)),
			StopCommandId: uuid.New(), CreatedAt: anchor.Add(time.Duration(index) * time.Hour), Demo: true, OperatorId: "demo",
			Stage: "finished", Outcome: outcome, DataQuality: "complete", SaveStatus: "saved", Parameters: createCommand().Parameters}
		record.Parameters.Valve = name
		body, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tests(id,record,stage) VALUES(?,?,'finished')`, record.TestId.String(), body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	query := "  клапан 100%_а  "
	from, before := anchor.Add(128*time.Hour).Format(time.RFC3339Nano), anchor.Add(131*time.Hour).Format(time.RFC3339Nano)
	outcome := contract.TestRecordOutcome("completed")
	params := contract.ListTestsParams{Q: &query, CreatedFrom: &from, CreatedBefore: &before, Outcome: &outcome, Limit: new(1)}
	first, next, total, err := store.ListTests(ctx, params)
	if err != nil || len(first) != 1 || next == nil || first[0].TestId.String() != "00000000-0000-4000-8000-000000000129" {
		t.Fatalf("first filtered page: %+v %v %v", first, next, err)
	}
	if total == nil || *total != 2 {
		t.Fatalf("total on first page: %v", total)
	}
	params.After = next
	second, next, total, err := store.ListTests(ctx, params)
	if err != nil || len(second) != 1 || next != nil || second[0].TestId.String() != "00000000-0000-4000-8000-000000000130" {
		t.Fatalf("second filtered page: %+v %v %v", second, next, err)
	}
	if total == nil || *total != 2 {
		t.Fatalf("total after cursor: %v", total)
	}
	if !second[0].CreatedAt.Equal(anchor.Add(129 * time.Hour)) {
		t.Fatal("timestamp precision lost")
	}
	query = "%_"
	all, _, _, err := store.ListTests(ctx, contract.ListTestsParams{Q: &query})
	if err != nil || len(all) != 4 {
		t.Fatalf("literal pattern: %d %v", len(all), err)
	}
	query = "ος"
	folded, _, _, err := store.ListTests(ctx, contract.ListTestsParams{Q: &query})
	if err != nil || len(folded) != 4 {
		t.Fatalf("Unicode simple fold: %d %v", len(folded), err)
	}
	query = second[0].TestId.String()
	byID, _, _, err := store.ListTests(ctx, contract.ListTestsParams{Q: &query})
	if err != nil || len(byID) != 1 || byID[0].TestId != second[0].TestId {
		t.Fatalf("ID search: %+v %v", byID, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, _, err := store.ListTests(canceled, contract.ListTestsParams{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	query = "%_"
	reopened, _, _, err := store.ListTests(ctx, contract.ListTestsParams{Q: &query})
	if err != nil || len(reopened) != 4 {
		t.Fatalf("reopen: %d %v", len(reopened), err)
	}
}

func TestCanceledReadTransactionReportsClientContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := readError(ctx, sql.ErrTxDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled transaction: %v", err)
	}
	if err := readError(context.Background(), sql.ErrTxDone); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("unrelated transaction error: %v", err)
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.AuditPage(ctx, uuid.New(), 0, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled page: %v", err)
	}
}

func TestHistoryAndCommandReadsSurviveReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[uuid.UUID]bool)
	var firstCommand uuid.UUID
	var firstTest uuid.UUID
	for i := 0; i < 3; i++ {
		command := createCommand()
		if i == 0 {
			firstCommand = command.CommandId
		}
		command.ExpectedRevision, err = store.Revision(ctx)
		if err != nil {
			t.Fatal(err)
		}
		created, err := store.Create(ctx, command)
		if err != nil || created.TestId == nil {
			t.Fatalf("create: %+v %v", created, err)
		}
		id := *created.TestId
		if i == 0 {
			firstTest = id
		}
		ids[id] = true
		status, err := store.Command(ctx, command.CommandId)
		if err != nil || status.Status != "completed" || status.TestId == nil || *status.TestId != id {
			t.Fatalf("command: %+v %v", status, err)
		}
		record, err := store.Test(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		reserved, err := store.Command(ctx, record.StopCommandId)
		if err != nil || reserved.Status != "unknown" {
			t.Fatalf("reserved stop: %+v %v", reserved, err)
		}
		if _, err := store.FinishStop(ctx, id, record.StopCommandId, StopDetails{Actor: "demo-operator", Reason: "operator", AcceptedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	revision, err := store.Revision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	late := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: revision, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := store.Start(ctx, firstTest, late, contract.PendingAction{ActionId: uuid.New(), TestId: firstTest, Instruction: "Позднее действие"}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("late start: %v", err)
	}
	firstAudit, err := store.AuditPage(ctx, firstTest, 0, 3)
	if err != nil || len(firstAudit.Items) != 3 || firstAudit.NextAfter == nil {
		t.Fatalf("audit first: %+v %v", firstAudit, err)
	}
	lastAudit, err := store.AuditPage(ctx, firstTest, *firstAudit.NextAfter, 3)
	if err != nil || len(lastAudit.Items) != 1 || lastAudit.NextAfter != nil || lastAudit.Items[0].Sequence <= *firstAudit.NextAfter+1 {
		t.Fatalf("audit gap: %+v %v", lastAudit, err)
	}
	rejected := createCommand()
	rejected.Parameters.AreaM2 = 0
	if _, err := store.Create(ctx, rejected); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("reject: %v", err)
	}
	status, err := store.Command(ctx, rejected.CommandId)
	if err != nil || status.Status != "rejected" || status.Reason == nil || *status.Reason != "invalid_command" {
		t.Fatalf("rejected: %+v %v", status, err)
	}
	if _, err := store.Command(ctx, uuid.New()); !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("missing command: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	reopened, err := store.Command(ctx, firstCommand)
	if err != nil || reopened.Status != "completed" {
		t.Fatalf("reopened command: %+v %v", reopened, err)
	}
	var after *uuid.UUID
	seen := make(map[uuid.UUID]bool)
	for {
		items, next, _, err := store.ListTests(ctx, contract.ListTestsParams{After: after, Limit: new(1)})
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range items {
			if !ids[record.TestId] || seen[record.TestId] {
				t.Fatalf("unexpected record: %+v", record)
			}
			seen[record.TestId] = true
		}
		if next == nil {
			break
		}
		after = next
	}
	if len(seen) != len(ids) {
		t.Fatalf("history missing records: %d/%d", len(seen), len(ids))
	}
	if _, _, _, err := store.ListTests(ctx, contract.ListTestsParams{After: nil, Limit: new(0)}); err == nil {
		t.Fatal("zero page size accepted")
	}
}

func TestMeasurementAndAuditPagesDistinguishEndAndMissing(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	created, err := store.Create(ctx, createCommand())
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	empty, err := store.MeasurementsPage(ctx, id, 0, 2)
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.NextAfter != nil {
		t.Fatalf("empty: %+v %v", empty, err)
	}
	if _, err := store.MeasurementsPage(ctx, uuid.New(), 0, 2); !errors.Is(err, ErrTestNotFound) {
		t.Fatalf("missing measurements: %v", err)
	}
	if _, err := store.AuditPage(ctx, uuid.New(), 0, 2); !errors.Is(err, ErrTestNotFound) {
		t.Fatalf("missing audit: %v", err)
	}
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	action := contract.PendingAction{ActionId: uuid.New(), TestId: id, Instruction: "Начать"}
	if _, err := store.Start(ctx, id, start, action); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Confirm(ctx, id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteStart(ctx, id, start.CommandId); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	measured, value := now.Format(time.RFC3339Nano), 10.0
	samples := make([]TimedMeasurement, 0, 3)
	for sequence := int64(1); sequence <= 3; sequence++ {
		m := contract.Measurement{Sequence: sequence, Channel: "dd1", Quantity: "pressure", Value: &value, Unit: "Pa", ObservedAt: now, MeasuredAt: &measured, Quality: "valid", Source: "simulator"}
		samples = append(samples, TimedMeasurement{Measurement: m, Elapsed: time.Duration(sequence) * time.Second})
	}
	if err := store.RecordMeasurements(ctx, id, samples); err != nil {
		t.Fatal(err)
	}
	first, err := store.MeasurementsPage(ctx, id, 0, 2)
	if err != nil || len(first.Items) != 2 || first.NextAfter == nil || *first.NextAfter != 2 {
		t.Fatalf("first: %+v %v", first, err)
	}
	last, err := store.MeasurementsPage(ctx, id, *first.NextAfter, 2)
	if err != nil || len(last.Items) != 1 || last.NextAfter != nil || last.Items[0].Sequence != 3 {
		t.Fatalf("last: %+v %v", last, err)
	}
	full, err := store.MeasurementsPage(ctx, id, 0, 3)
	if err != nil || len(full.Items) != 3 || full.NextAfter != nil {
		t.Fatalf("exact boundary: %+v %v", full, err)
	}
	for startSequence := int64(4); startSequence <= 501; {
		batch := make([]TimedMeasurement, 0, 128)
		for len(batch) < 128 && startSequence <= 501 {
			m := samples[0].Measurement
			m.Sequence = startSequence
			batch = append(batch, TimedMeasurement{Measurement: m, Elapsed: time.Duration(startSequence) * time.Second})
			startSequence++
		}
		if err := store.RecordMeasurements(ctx, id, batch); err != nil {
			t.Fatal(err)
		}
	}
	large, err := store.MeasurementsPage(ctx, id, 0, 500)
	if err != nil || len(large.Items) != 500 || large.NextAfter == nil || *large.NextAfter != 500 {
		t.Fatalf("maximum page: %d %+v %v", len(large.Items), large.NextAfter, err)
	}
	final, err := store.MeasurementsPage(ctx, id, *large.NextAfter, 500)
	if err != nil || len(final.Items) != 1 || final.NextAfter != nil || final.Items[0].Sequence != 501 {
		t.Fatalf("maximum continuation: %+v %v", final, err)
	}
	audit, err := store.AuditPage(ctx, id, 0, 1)
	if err != nil || len(audit.Items) != 1 || audit.NextAfter == nil || *audit.NextAfter != audit.Items[0].Sequence {
		t.Fatalf("audit: %+v %v", audit, err)
	}
	if _, err := store.AuditPage(ctx, id, 0, 501); err == nil {
		t.Fatal("oversized page accepted")
	}
}
