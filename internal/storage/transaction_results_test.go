package storage

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

func TestWorkflowAuditFailureRollsBack(t *testing.T) {
	for _, operation := range []string{"start", "confirm", "completeStart", "complete", "finishStop"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			store, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			created, err := store.Create(ctx, createCommand())
			if err != nil {
				t.Fatal(err)
			}
			id := *created.TestId
			action := contract.PendingAction{ActionId: uuid.New(), TestId: id, Instruction: "Подтвердите измерение"}
			start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
			confirm := contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}
			if operation != "start" {
				if _, err := store.Start(ctx, id, start, action); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "completeStart" || operation == "complete" {
				if _, err := store.Confirm(ctx, id, action.ActionId, confirm); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "complete" {
				if _, err := store.CompleteStart(ctx, id, start.CommandId); err != nil {
					t.Fatal(err)
				}
				now := time.Now().UTC()
				measured, value := now.Format(time.RFC3339Nano), 12.5
				sample := contract.Measurement{Sequence: 1, Channel: "dd1", Quantity: "pressure", Unit: "Pa", ObservedAt: now, MeasuredAt: &measured, Value: &value, Quality: "valid", Source: "simulator"}
				second := sample
				second.Sequence = 2
				if err := store.RecordMeasurements(ctx, id, []TimedMeasurement{{Measurement: sample}, {Measurement: second, Elapsed: time.Second}}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := store.Test(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			revision, err := store.Revision(ctx)
			if err != nil {
				t.Fatal(err)
			}
			audit, err := store.Audit(ctx, id, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			pending, err := store.PendingAction(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			commands := make(map[uuid.UUID]contract.CommandStatus)
			for _, commandID := range []uuid.UUID{start.CommandId, confirm.CommandId, before.StopCommandId} {
				commands[commandID], err = store.Command(ctx, commandID)
				if err != nil && !errors.Is(err, ErrCommandNotFound) {
					t.Fatal(err)
				}
			}
			if _, err := store.db.Exec(`CREATE TRIGGER reject_workflow_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "start":
				result, writeErr := store.Start(ctx, id, start, action)
				err = writeErr
				if result.Command != nil || result.Saved != nil || result.Applied {
					t.Fatalf("rollback returned confirmation: %+v", result)
				}
			case "confirm":
				result, writeErr := store.Confirm(ctx, id, action.ActionId, confirm)
				err = writeErr
				if result.Command != nil || result.Saved != nil || result.Applied {
					t.Fatalf("rollback returned confirmation: %+v", result)
				}
			case "completeStart":
				result, writeErr := store.CompleteStart(ctx, id, start.CommandId)
				err = writeErr
				if result.Command != nil || result.Saved != nil || result.Applied {
					t.Fatalf("rollback returned confirmation: %+v", result)
				}
			case "complete":
				err = store.Complete(ctx, id, time.Second)
			case "finishStop":
				_, err = store.FinishStop(ctx, id, before.StopCommandId, StopDetails{Actor: "demo-operator", Reason: "operator", AcceptedAt: time.Now()})
			}
			if err == nil || !strings.Contains(err.Error(), "audit unavailable") {
				t.Fatalf("must reach the injected audit failure: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reopened.Close() }()
			after, err := reopened.Test(ctx, id)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("record after rollback/reopen: %+v %v", after, err)
			}
			if actual, err := reopened.Revision(ctx); err != nil || actual != revision {
				t.Fatalf("revision: %d want %d, %v", actual, revision, err)
			}
			if actual, err := reopened.Audit(ctx, id, 0, 100); err != nil || !reflect.DeepEqual(audit, actual) {
				t.Fatalf("audit after rollback: %+v %v", actual, err)
			}
			if actual, err := reopened.PendingAction(ctx, id); err != nil || !reflect.DeepEqual(pending, actual) {
				t.Fatalf("action after rollback: %+v %v", actual, err)
			}
			for commandID, expected := range commands {
				actual, err := reopened.Command(ctx, commandID)
				if expected.CommandId == uuid.Nil && errors.Is(err, ErrCommandNotFound) {
					continue
				}
				if err != nil || !reflect.DeepEqual(expected, actual) {
					t.Fatalf("command after rollback: %+v want %+v, %v", actual, expected, err)
				}
			}
		})
	}
}

func TestStartOperationResultsSurviveReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	created, err := store.Create(ctx, createCommand())
	if err != nil {
		t.Fatal(err)
	}
	id := *created.TestId
	action := contract.PendingAction{ActionId: uuid.New(), TestId: id, Instruction: "Подтвердите измерение"}
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	confirm := contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}
	operations := []func() (OperationResult, error){
		func() (OperationResult, error) { return store.Start(ctx, id, start, action) },
		func() (OperationResult, error) { return store.Confirm(ctx, id, action.ActionId, confirm) },
		func() (OperationResult, error) { return store.CompleteStart(ctx, id, start.CommandId) },
	}
	for index, operation := range operations {
		result, err := operation()
		if err != nil || !result.Applied || result.Replayed || result.Command == nil || result.Saved == nil || result.Saved.Revision != int64(index+2) {
			t.Fatalf("operation %d: %+v %v", index, result, err)
		}
		audit, err := store.Audit(ctx, id, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := operation()
		if err != nil || replay.Applied || !replay.Replayed || !reflect.DeepEqual(result.Command, replay.Command) {
			t.Fatalf("replay %d: %+v %v", index, replay, err)
		}
		if index < 2 && replay.Saved != nil {
			t.Fatal("command replay returned a historical snapshot")
		}
		if index == 2 && !reflect.DeepEqual(result.Saved, replay.Saved) {
			t.Fatal("no-op lost the current saved state")
		}
		if actual, err := store.Audit(ctx, id, 0, 100); err != nil || !reflect.DeepEqual(audit, actual) {
			t.Fatalf("replay audit: %+v %v", actual, err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		store, err = Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		assertSavedOperation(t, store, result)
	}
	// A completed Start no-op reads today's record, including a later Stop.
	record, err := store.Test(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishStop(ctx, id, record.StopCommandId, StopDetails{Actor: "demo-operator", Reason: "operator", AcceptedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	result, err := store.CompleteStart(ctx, id, start.CommandId)
	if err != nil || result.Applied || !result.Replayed || result.Saved == nil || result.Saved.Record.Stage != "finished" {
		t.Fatalf("completed start after Stop: %+v %v", result, err)
	}
	assertSavedOperation(t, store, result)
}

func assertSavedOperation(t *testing.T, store *Store, result OperationResult) {
	t.Helper()
	if result.Saved == nil {
		t.Fatal("missing saved state")
	}
	ctx := context.Background()
	record, err := store.Test(ctx, result.Saved.Record.TestId)
	if err != nil || !reflect.DeepEqual(record, result.Saved.Record) {
		t.Fatalf("saved record: %+v want %+v, %v", record, result.Saved.Record, err)
	}
	if revision, err := store.Revision(ctx); err != nil || revision != result.Saved.Revision {
		t.Fatalf("saved revision: %d want %d, %v", revision, result.Saved.Revision, err)
	}
	if action, err := store.PendingAction(ctx, record.TestId); err != nil || !reflect.DeepEqual(action, result.Saved.PendingAction) {
		t.Fatalf("saved action: %+v want %+v, %v", action, result.Saved.PendingAction, err)
	}
}

func TestOperatorResultsDistinguishRejectionReplayAndCancellation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	created, err := store.Create(ctx, createCommand())
	if err != nil {
		t.Fatal(err)
	}
	id := *created.TestId
	action := contract.PendingAction{ActionId: uuid.New(), TestId: id, Instruction: "Подтвердите измерение"}
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 0, ValveClosed: true, ConnectionConfirmed: true}
	for index := range 2 {
		result, err := store.Start(ctx, id, start, action)
		if !errors.Is(err, ErrRevision) || result.Command == nil || result.Command.Status != "rejected" || result.Applied || result.Saved != nil || result.Replayed != (index == 1) {
			t.Fatalf("rejection %d: %+v %v", index, result, err)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	for _, operation := range []func() (OperationResult, error){
		func() (OperationResult, error) { return store.Start(ctx, id, start, action) },
		func() (OperationResult, error) {
			return store.Confirm(ctx, id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 1})
		},
		func() (OperationResult, error) { return store.CompleteStart(ctx, id, start.CommandId) },
	} {
		result, err := operation()
		if !errors.Is(err, context.Canceled) || result.Command != nil || result.Saved != nil || result.Applied || result.Replayed {
			t.Fatalf("cancellation: %+v %v", result, err)
		}
	}
}
