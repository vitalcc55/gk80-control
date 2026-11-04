package demo

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

// This controlled repository reports revisions that cannot be inferred from
// command.ExpectedRevision. SQLite content is checked separately in storage.
type reportedStartResults struct {
	*storage.Store
	entered chan struct{}
	release chan struct{}
	noOp    bool
}

type lostStartResult struct {
	*storage.Store
	operation   string
	stopEntered chan struct{}
	stopRelease chan struct{}
}

func (s *lostStartResult) Start(ctx context.Context, id uuid.UUID, cmd contract.StartTestCommand, action contract.PendingAction) (storage.OperationResult, error) {
	result, err := s.Store.Start(ctx, id, cmd, action)
	if err == nil && s.operation == "start" {
		return storage.OperationResult{}, context.DeadlineExceeded
	}
	return result, err
}

func (s *lostStartResult) Confirm(ctx context.Context, id, action uuid.UUID, cmd contract.ConfirmActionCommand) (storage.OperationResult, error) {
	result, err := s.Store.Confirm(ctx, id, action, cmd)
	if err == nil && s.operation == "confirm" {
		return storage.OperationResult{}, context.DeadlineExceeded
	}
	return result, err
}

func (s *lostStartResult) CompleteStart(ctx context.Context, id, cmd uuid.UUID) (storage.OperationResult, error) {
	result, err := s.Store.CompleteStart(ctx, id, cmd)
	if err == nil && s.operation == "completeStart" {
		return storage.OperationResult{}, context.DeadlineExceeded
	}
	return result, err
}

func (s *lostStartResult) FinishStop(ctx context.Context, id, cmd uuid.UUID, details storage.StopDetails) (contract.CommandStatus, error) {
	close(s.stopEntered)
	<-s.stopRelease
	return s.Store.FinishStop(ctx, id, cmd, details)
}

func TestUnknownCommittedStartResultDoesNotClaimSavedOrRestart(t *testing.T) {
	for _, operation := range []string{"start", "confirm", "completeStart"} {
		t.Run(operation, func(t *testing.T) {
			store, err := storage.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			wrapped := &lostStartResult{Store: store, operation: operation, stopEntered: make(chan struct{}), stopRelease: make(chan struct{})}
			defer func() {
				select {
				case <-wrapped.stopRelease:
				default:
					close(wrapped.stopRelease)
				}
			}()
			simulator := &countedSimulator{}
			engine := newTestEngine(t, wrapped, simulator, uuid.New())
			created, err := engine.Create(context.Background(), command(3600))
			if err != nil {
				t.Fatal(err)
			}
			id := *created.TestId
			start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
			confirm := contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}
			_, err = engine.Start(context.Background(), id, start)
			if operation != "start" {
				if err != nil {
					t.Fatal(err)
				}
				action := engine.State().PendingAction
				_, err = engine.Confirm(context.Background(), id, action.ActionId, confirm)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost result: %v", err)
			}
			<-wrapped.stopEntered
			state := engine.State()
			if state.Record.SaveStatus == "saved" || state.Record.Stage != "finished" || simulator.stops.Load() != 1 {
				t.Fatalf("unconfirmed operation: %+v stops=%d", state, simulator.stops.Load())
			}
			expectedStarts := int32(0)
			if operation == "completeStart" {
				expectedStarts = 1
			}
			if simulator.starts.Load() != expectedStarts {
				t.Fatalf("starts=%d want %d", simulator.starts.Load(), expectedStarts)
			}
			// The discarded response really followed a SQLite commit.
			record, err := store.Test(context.Background(), id)
			if err != nil || record.Stage != "running" {
				t.Fatalf("committed record: %+v %v", record, err)
			}
			status, err := store.Command(context.Background(), start.CommandId)
			if err != nil || (operation == "completeStart" && status.Status != "completed") || (operation != "completeStart" && status.Status != "accepted") {
				t.Fatalf("committed Start: %+v %v", status, err)
			}
			if operation != "start" {
				status, err := store.Command(context.Background(), confirm.CommandId)
				if err != nil || status.Status != "completed" {
					t.Fatalf("committed Confirm: %+v %v", status, err)
				}
				if action, err := store.PendingAction(context.Background(), id); err != nil || action != nil {
					t.Fatalf("confirmed action: %+v %v", action, err)
				}
			}
			close(wrapped.stopRelease)
			waitFor(t, engine, func(state State) bool { return state.Record.SaveStatus == "saved" })
			if err := engine.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			if simulator.starts.Load() != expectedStarts || simulator.stops.Load() != 1 {
				t.Fatal("recovery repeated equipment action")
			}
		})
	}
}

func (s *reportedStartResults) Start(ctx context.Context, id uuid.UUID, cmd contract.StartTestCommand, action contract.PendingAction) (storage.OperationResult, error) {
	action.ActionId = uuid.New()
	action.Instruction = "Сохранённая инструкция"
	result, err := s.Store.Start(ctx, id, cmd, action)
	if result.Saved != nil {
		result.Saved.Revision = 41
	}
	return result, err
}

func (s *reportedStartResults) Confirm(ctx context.Context, id, action uuid.UUID, cmd contract.ConfirmActionCommand) (storage.OperationResult, error) {
	result, err := s.Store.Confirm(ctx, id, action, cmd)
	if result.Saved != nil {
		result.Saved.Revision = 73
	}
	return result, err
}

func (s *reportedStartResults) CompleteStart(ctx context.Context, id, cmd uuid.UUID) (storage.OperationResult, error) {
	close(s.entered)
	<-s.release
	result, err := s.Store.CompleteStart(ctx, id, cmd)
	if err == nil && s.noOp {
		result, err = s.Store.CompleteStart(ctx, id, cmd)
	}
	if result.Saved != nil {
		result.Saved.Revision = 97
	}
	return result, err
}

func TestEngineUsesReportedStartResults(t *testing.T) {
	for _, noOp := range []bool{false, true} {
		t.Run(map[bool]string{false: "applied", true: "completed_no_op"}[noOp], func(t *testing.T) {
			store, err := storage.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			wrapped := &reportedStartResults{Store: store, entered: make(chan struct{}), release: make(chan struct{}), noOp: noOp}
			simulator := &countedSimulator{}
			engine := newTestEngine(t, wrapped, simulator, uuid.New())
			created, err := engine.Create(context.Background(), command(3600))
			if err != nil {
				t.Fatal(err)
			}
			id := *created.TestId
			if _, err := engine.Start(context.Background(), id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
				t.Fatal(err)
			}
			state := engine.State()
			if state.Revision != 41 || state.PendingAction == nil || state.PendingAction.Instruction != "Сохранённая инструкция" {
				t.Fatalf("Start result: %+v", state)
			}
			confirmDone := make(chan error, 1)
			go func() {
				_, err := engine.Confirm(context.Background(), id, state.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2})
				confirmDone <- err
			}()
			<-wrapped.entered
			state = engine.State()
			if state.Revision != 73 || state.PendingAction != nil || state.Record.SaveStatus != "pending" {
				t.Fatalf("Confirm result and operational start: %+v", state)
			}
			close(wrapped.release)
			if err := <-confirmDone; err != nil {
				t.Fatal(err)
			}
			state = engine.State()
			if state.Revision != 97 || state.Record.SaveStatus != "saved" || simulator.starts.Load() != 1 {
				t.Fatalf("CompleteStart result: %+v starts=%d", state, simulator.starts.Load())
			}
			if err := engine.Shutdown(context.Background(), "test_cleanup"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
