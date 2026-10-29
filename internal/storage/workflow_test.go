package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

func TestDemoRecordTransitionsAndMeasurementsSurviveReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	initialStore := store
	t.Cleanup(func() { _ = initialStore.Close() })
	created, err := store.Create(ctx, createCommand())
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %+v %v", created, err)
	}
	testID := *created.TestId
	action := contract.PendingAction{ActionId: uuid.New(), TestId: testID, Instruction: "Подтвердите начало демонстрационного измерения"}
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	if status, err := store.Start(ctx, testID, start, action); err != nil || status.Status != "accepted" {
		t.Fatal(err)
	}
	pending, err := store.PendingAction(ctx, testID)
	if err != nil || pending == nil || *pending != action {
		t.Fatalf("pending: %+v %v", pending, err)
	}
	confirm := contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}
	if _, err := store.Confirm(ctx, testID, action.ActionId, confirm); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteStart(ctx, testID, start.CommandId); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-2 * time.Second)
	measured := now.Format(time.RFC3339Nano)
	pressure := 12.5
	sample := contract.Measurement{Sequence: 1, Channel: "dd1", Quantity: "pressure", Unit: "Pa", ObservedAt: now, MeasuredAt: &measured, Value: &pressure, Quality: "valid", Source: "simulator"}
	secondTime := now.Add(-time.Hour)
	secondMeasured := secondTime.Format(time.RFC3339Nano)
	second := sample
	second.Sequence = 2
	second.ObservedAt = secondTime
	second.MeasuredAt = &secondMeasured
	if err := store.RecordMeasurements(ctx, testID, []TimedMeasurement{{Measurement: sample, Elapsed: 5 * time.Millisecond}, {Measurement: second, Elapsed: time.Second + 5*time.Millisecond}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, testID, time.Millisecond); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("premature completion: %v", err)
	}
	if err := store.Complete(ctx, testID, time.Second+5*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if result, err := store.Start(ctx, testID, start, action); err != nil || result.Status != "completed" {
		t.Fatalf("start retry after completion: %+v %v", result, err)
	}
	if _, err := store.Confirm(ctx, testID, action.ActionId, confirm); err != nil {
		t.Fatalf("confirm retry after completion: %v", err)
	}
	otherAction := uuid.New()
	if _, err := store.Confirm(ctx, testID, otherAction, confirm); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("route collision: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	record, err := store.Test(ctx, testID)
	if err != nil || record.Stage != "finished" || record.Outcome != "completed" || record.DataQuality != "complete" {
		t.Fatalf("reopened record: %+v %v", record, err)
	}
	samples, err := store.Measurements(ctx, testID, 0, 100)
	if err != nil || len(samples) != 2 || samples[0].Value == nil || *samples[0].Value != pressure || samples[0].MeasuredAt == nil || *samples[0].MeasuredAt != measured {
		t.Fatalf("reopened measurements: %+v %v", samples, err)
	}
	if samples[1].MeasuredAt == nil || *samples[1].MeasuredAt != secondMeasured {
		t.Fatalf("absolute clock change not preserved: %+v", samples[1])
	}
	audit, err := store.Audit(ctx, testID, 0, 100)
	if err != nil || len(audit) != 5 {
		t.Fatalf("audit: %+v %v", audit, err)
	}
	if audit[2].Instruction == nil || *audit[2].Instruction != action.Instruction || audit[2].ActionId == nil || *audit[2].ActionId != action.ActionId {
		t.Fatalf("confirmed instruction missing from history: %+v", audit[2])
	}
}

func TestRecoveryDoesNotResumeActiveDemo(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	initialStore := store
	t.Cleanup(func() { _ = initialStore.Close() })
	created, err := store.Create(ctx, createCommand())
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	action := contract.PendingAction{ActionId: uuid.New(), TestId: id, Instruction: "Подтвердите измерение"}
	cmd := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := store.Start(ctx, id, cmd, action); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if status, err := store.Start(ctx, id, cmd, action); err != nil || status.Status != "unknown" {
		t.Fatalf("interrupted command status: %+v %v", status, err)
	}
	record, err := store.Test(ctx, id)
	if err != nil || record.Stage != "finished" || record.Outcome != "unknown" || record.DataQuality != "incomplete" || record.FinishedAt != nil {
		t.Fatalf("recovery: %+v %v", record, err)
	}
	pending, err := store.PendingAction(ctx, id)
	if err != nil || pending != nil {
		t.Fatalf("recovered pending action: %+v %v", pending, err)
	}
	audit, err := store.Audit(ctx, id, 0, 100)
	if err != nil || len(audit) != 4 || audit[2].Outcome != "unknown" || audit[2].CommandId == nil || *audit[2].CommandId != cmd.CommandId || audit[3].Action != "recovered" {
		t.Fatalf("recovery audit: %+v %v", audit, err)
	}
}

func TestConfirmedStopIsPersistedOnceAndReopens(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	initial := store
	t.Cleanup(func() { _ = initial.Close() })
	created, err := store.Create(ctx, createCommand())
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	record, err := store.Test(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	action := contract.PendingAction{ActionId: uuid.New(), TestId: id, Instruction: "Подтвердите измерение"}
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := store.Start(ctx, id, start, action); err != nil {
		t.Fatal(err)
	}
	stopped, err := store.FinishStop(ctx, id, record.StopCommandId)
	if err != nil || stopped.Status != "completed" {
		t.Fatalf("stop: %+v %v", stopped, err)
	}
	if _, err := store.FinishStop(ctx, uuid.New(), record.StopCommandId); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("stop path conflict: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	stopped, err = store.FinishStop(ctx, id, record.StopCommandId)
	if err != nil || stopped.Status != "completed" {
		t.Fatalf("stop replay: %+v %v", stopped, err)
	}
	result, err := store.Test(ctx, id)
	if err != nil || result.Stage != "finished" || result.Outcome != "interrupted" || result.DataQuality != "incomplete" {
		t.Fatalf("reopened stop: %+v %v", result, err)
	}
	audit, err := store.Audit(ctx, id, 0, 100)
	if err != nil || len(audit) != 4 || audit[2].Outcome != "unknown" || audit[2].CommandId == nil || *audit[2].CommandId != start.CommandId || audit[3].Action != "stop" {
		t.Fatalf("stop audit: %+v %v", audit, err)
	}
}

func TestRejectedActionIsAuditedOnce(t *testing.T) {
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
	action := contract.PendingAction{ActionId: uuid.New(), TestId: id, Instruction: "Подтвердите измерение"}
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := store.Start(ctx, id, start, action); err != nil {
		t.Fatal(err)
	}
	wrongID := uuid.New()
	command := contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}
	for range 2 {
		if _, err := store.Confirm(ctx, id, wrongID, command); !errors.Is(err, ErrActionMismatch) {
			t.Fatalf("rejected confirmation: %v", err)
		}
	}
	events, err := store.Audit(ctx, id, 0, 100)
	if err != nil || len(events) != 3 || events[2].Outcome != "rejected" || events[2].CommandId == nil || *events[2].CommandId != command.CommandId || events[2].ActionId == nil || *events[2].ActionId != wrongID || events[2].Reason == nil || *events[2].Reason != "action_mismatch" {
		t.Fatalf("rejection audit: %+v %v", events, err)
	}
}

func TestIncompletePressureSamplesCannotProduceCompleteResult(t *testing.T) {
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
	action := contract.PendingAction{ActionId: uuid.New(), TestId: id, Instruction: "Подтвердите измерение"}
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := store.Start(ctx, id, start, action); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Confirm(ctx, id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteStart(ctx, id, start.CommandId); err != nil {
		t.Fatal(err)
	}
	sample := contract.Measurement{Sequence: 1, Channel: "dd1", Quantity: "pressure", Unit: "Pa", ObservedAt: time.Now().UTC(), Quality: "unavailable", Source: "simulator"}
	if err := store.RecordMeasurements(ctx, id, []TimedMeasurement{{Measurement: sample}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, id, time.Second); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("unavailable pressure accepted as complete: %v", err)
	}
	record, err := store.Test(ctx, id)
	if err != nil || record.Stage != "running" || record.DataQuality != "pending" {
		t.Fatalf("incorrect result: %+v %v", record, err)
	}
}

func TestMeasurementBatchFailureRollsBackEntireBatch(t *testing.T) {
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
	action := contract.PendingAction{ActionId: uuid.New(), TestId: id, Instruction: "Подтвердите измерение"}
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := store.Start(ctx, id, start, action); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Confirm(ctx, id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteStart(ctx, id, start.CommandId); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_second BEFORE INSERT ON measurements WHEN NEW.sequence = 2 BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	timestamp := time.Now().UTC()
	measured := timestamp.Format(time.RFC3339Nano)
	value := 1.0
	first := contract.Measurement{Sequence: 1, Channel: "dd1", Quantity: "pressure", Unit: "Pa", ObservedAt: timestamp, MeasuredAt: &measured, Value: &value, Quality: "valid", Source: "simulator"}
	second := first
	second.Sequence = 2
	if err := store.RecordMeasurements(ctx, id, []TimedMeasurement{{Measurement: first}, {Measurement: second, Elapsed: time.Second}}); err == nil {
		t.Fatal("expected batch failure")
	}
	samples, err := store.Measurements(ctx, id, 0, 100)
	if err != nil || len(samples) != 0 {
		t.Fatalf("partial batch persisted: %+v %v", samples, err)
	}
	if err := store.RecordMeasurements(ctx, id, []TimedMeasurement{{Measurement: first}}); !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("new write was not blocked: %v", err)
	}
}

func TestRejectedStartsDoNotBlockSuccessfulMeasurement(t *testing.T) {
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
	action := contract.PendingAction{ActionId: uuid.New(), TestId: id, Instruction: "Подтвердите измерение"}
	stale := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 0, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := store.Start(ctx, id, stale, action); !errors.Is(err, ErrRevision) {
		t.Fatalf("stale start: %v", err)
	}
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := store.Start(ctx, id, start, action); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Confirm(ctx, id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteStart(ctx, id, start.CommandId); err != nil {
		t.Fatal(err)
	}
	another := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 4, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := store.Start(ctx, id, another, action); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("duplicate start: %v", err)
	}
	observed := time.Now().UTC()
	measured := observed.Format(time.RFC3339Nano)
	value := 1.0
	sample := contract.Measurement{Sequence: 1, Channel: "dd1", Quantity: "pressure", Unit: "Pa", ObservedAt: observed, MeasuredAt: &measured, Value: &value, Quality: "valid", Source: "simulator"}
	if err := store.RecordMeasurements(ctx, id, []TimedMeasurement{{Measurement: sample}}); err != nil {
		t.Fatalf("rejected start blocked samples: %v", err)
	}
}

func TestValidSampleMayBeObservedAfterMeasurement(t *testing.T) {
	observed := time.Now().UTC()
	other := observed.Add(-time.Second).Format(time.RFC3339Nano)
	value := 1.0
	sample := contract.Measurement{Sequence: 1, Channel: "dd1", Quantity: "pressure", Unit: "Pa", ObservedAt: observed, MeasuredAt: &other, Value: &value, Quality: "valid", Source: "simulator"}
	if !validMeasurement(sample) {
		t.Fatal("valid delayed observation was rejected")
	}
}
