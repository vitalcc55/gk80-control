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
				if err := store.CompleteStart(ctx, id, start.CommandId); err != nil {
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
				_, err = store.Start(ctx, id, start, action)
			case "confirm":
				_, err = store.Confirm(ctx, id, action.ActionId, confirm)
			case "completeStart":
				err = store.CompleteStart(ctx, id, start.CommandId)
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
