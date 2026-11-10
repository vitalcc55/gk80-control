package demo

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

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

type finalResultRepository struct {
	*storage.Store
	lost         bool
	failBefore   bool
	failedBefore atomic.Bool
	committed    atomic.Bool
	calls        atomic.Int32
	reads        atomic.Int32
	entered      chan struct{}
	release      chan struct{}
}

func (s *finalResultRepository) report(result storage.OperationResult, err error) (storage.OperationResult, error) {
	s.calls.Add(1)
	if err != nil {
		return result, err
	}
	s.committed.Store(true)
	if s.entered != nil {
		close(s.entered)
		<-s.release
	}
	if s.lost {
		return storage.OperationResult{}, context.DeadlineExceeded
	}
	if result.Saved != nil {
		result.Saved.Revision = 101
	}
	return result, nil
}

func TestLateFinalResultDoesNotReplaceNewRun(t *testing.T) {
	for _, operation := range []string{"complete", "finishStop"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			store, err := storage.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			wrapped := &finalResultRepository{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
			defer func() {
				select {
				case <-wrapped.release:
				default:
					close(wrapped.release)
				}
			}()
			simulator := &countedSimulator{}
			engine := newTestEngine(t, wrapped, simulator, uuid.New())
			advanceMeasurementOnNextSample(engine)
			created, err := engine.Create(ctx, command(60))
			if err != nil {
				t.Fatal(err)
			}
			id := *created.TestId
			if _, err := engine.Start(ctx, id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
				t.Fatal(err)
			}
			if operation == "complete" {
				action := engine.State().PendingAction
				if _, err := engine.Confirm(ctx, id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}); err != nil {
					t.Fatal(err)
				}
			} else if _, err := engine.Stop(ctx, id, engine.State().Record.StopCommandId); err != nil {
				t.Fatal(err)
			}
			<-wrapped.entered
			// Isolate the late-result identity guard without opening another real
			// test while finalization is pending (normal admission forbids that).
			engine.mu.Lock()
			old := engine.active
			done := old.finishDone
			if operation == "finishStop" {
				done = old.stopDone
			}
			replacement := &run{record: contract.TestRecord{TestId: uuid.New(), Stage: "preparing", SaveStatus: "pending"}, revision: 333}
			engine.active = replacement
			engine.revision = replacement.revision
			engine.mu.Unlock()
			close(wrapped.release)
			if err := waitForStop(ctx, done); err != nil {
				t.Fatal(err)
			}
			state := engine.State()
			if state.Record.TestId != replacement.record.TestId || state.Record.Stage != "preparing" || state.Record.SaveStatus != "pending" || state.Revision != 333 {
				t.Fatalf("late result replaced new run: %+v", state)
			}
			if operation == "complete" && (simulator.starts.Load() != 1 || simulator.finishes.Load() != 1 || simulator.stops.Load() != 0) {
				t.Fatal("late completion repeated equipment")
			}
			if operation == "finishStop" && (simulator.starts.Load() != 0 || simulator.stops.Load() != 1) {
				t.Fatal("late Stop repeated equipment")
			}
		})
	}
}

func (s *finalResultRepository) Complete(ctx context.Context, id uuid.UUID, elapsed time.Duration) (storage.OperationResult, error) {
	if s.failBefore && !s.failedBefore.Swap(true) {
		return storage.OperationResult{}, context.DeadlineExceeded
	}
	return s.report(s.Store.Complete(ctx, id, elapsed))
}

func (s *finalResultRepository) FinishStop(ctx context.Context, id, cmd uuid.UUID, details storage.StopDetails) (storage.OperationResult, error) {
	if s.failBefore && !s.failedBefore.Swap(true) {
		return storage.OperationResult{}, context.DeadlineExceeded
	}
	return s.report(s.Store.FinishStop(ctx, id, cmd, details))
}

func (s *finalResultRepository) Test(ctx context.Context, id uuid.UUID) (contract.TestRecord, error) {
	if s.committed.Load() {
		s.reads.Add(1)
		if !s.lost {
			return contract.TestRecord{}, errors.New("unexpected read after confirmed commit")
		}
	}
	return s.Store.Test(ctx, id)
}

func (s *finalResultRepository) Revision(ctx context.Context) (int64, error) {
	if s.committed.Load() {
		s.reads.Add(1)
		if !s.lost {
			return 0, errors.New("unexpected read after confirmed commit")
		}
	}
	return s.Store.Revision(ctx)
}

func TestEngineFinalizationUsesCommittedResult(t *testing.T) {
	for _, operation := range []string{"complete", "finishStop"} {
		for _, mode := range []string{"confirmed", "unknown_after_commit", "before_commit"} {
			t.Run(operation+"/"+mode, func(t *testing.T) {
				ctx := context.Background()
				store, err := storage.Open(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = store.Close() }()
				lost := mode == "unknown_after_commit"
				beforeCommit := mode == "before_commit"
				wrapped := &finalResultRepository{Store: store, lost: lost, failBefore: beforeCommit}
				simulator := &countedSimulator{}
				engine := newTestEngine(t, wrapped, simulator, uuid.New())
				advanceMeasurementOnNextSample(engine)
				created, err := engine.Create(ctx, command(60))
				if err != nil {
					t.Fatal(err)
				}
				id := *created.TestId
				if _, err := engine.Start(ctx, id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
					t.Fatal(err)
				}
				if operation == "complete" {
					action := engine.State().PendingAction
					if _, err := engine.Confirm(ctx, id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := engine.Stop(ctx, id, engine.State().Record.StopCommandId); err != nil {
						t.Fatal(err)
					}
				}
				wanted := contract.TestRecordSaveStatus("saved")
				if lost || beforeCommit {
					wanted = "unknown"
				}
				state := waitFor(t, engine, func(state State) bool {
					return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == wanted
				})
				engine.mu.Lock()
				done := engine.active.finishDone
				if operation == "finishStop" {
					done = engine.active.stopDone
				}
				engine.mu.Unlock()
				if err := waitForStop(ctx, done); err != nil {
					t.Fatal(err)
				}
				if beforeCommit {
					if wrapped.committed.Load() {
						t.Fatal("first attempt unexpectedly committed")
					}
					if operation == "complete" && state.Record.DataQuality != "unknown" {
						t.Fatalf("unconfirmed quality: %+v", state.Record)
					}
					if err := engine.Refresh(ctx); err != nil {
						t.Fatal(err)
					}
					state = engine.State()
					if state.Record.SaveStatus != "saved" {
						t.Fatalf("recovered commit not applied: %+v", state)
					}
				}
				if lost {
					record, err := store.Test(ctx, id)
					if err != nil || record.Stage != "finished" || record.SaveStatus != "saved" {
						t.Fatalf("actual committed record: %+v %v", record, err)
					}
					if err := engine.Refresh(ctx); err != nil {
						t.Fatal(err)
					}
					state = engine.State()
					revision, err := store.Revision(ctx)
					if err != nil || state.Record.SaveStatus != "saved" || state.Revision != revision || wrapped.reads.Load() != 2 {
						t.Fatalf("read-only recovery: %+v reads=%d %v", state, wrapped.reads.Load(), err)
					}
				} else if state.Revision != 101 || wrapped.reads.Load() != 0 {
					t.Fatalf("confirmed result: %+v reads=%d", state, wrapped.reads.Load())
				}
				if wrapped.calls.Load() != 1 {
					t.Fatalf("finalization was repeated: %d", wrapped.calls.Load())
				}
				quality := contract.TestRecordDataQuality("complete")
				if operation == "finishStop" {
					quality = "incomplete"
				}
				if state.Record.DataQuality != quality {
					t.Fatalf("final quality: %+v", state.Record)
				}
				if operation == "complete" && (simulator.starts.Load() != 1 || simulator.finishes.Load() != 1 || simulator.stops.Load() != 0) {
					t.Fatal("completion/recovery repeated equipment")
				}
				if operation == "finishStop" && (simulator.starts.Load() != 0 || simulator.stops.Load() != 1) {
					t.Fatal("Stop/recovery repeated equipment")
				}
			})
		}
	}
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

func (s *lostStartResult) FinishStop(ctx context.Context, id, cmd uuid.UUID, details storage.StopDetails) (storage.OperationResult, error) {
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
			created, err := engine.Create(context.Background(), command(300))
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
			created, err := engine.Create(context.Background(), command(300))
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
			if state.Revision != 97 || state.Record.Stage != "running" || simulator.starts.Load() != 1 {
				t.Fatalf("CompleteStart result: %+v starts=%d", state, simulator.starts.Load())
			}
			if err := engine.Shutdown(context.Background(), "test_cleanup"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
