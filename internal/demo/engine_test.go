package demo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

func newTestEngine(t *testing.T, store Repository, equipment Equipment, runID uuid.UUID) *Engine {
	t.Helper()
	engine, err := NewEngine(context.Background(), store, equipment, runID)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func command(seconds float64) contract.CreateTestCommand {
	return contract.CreateTestCommand{CommandId: uuid.New(), Parameters: contract.TestParameters{Valve: "Демо клапан", AreaM2: 0.01, PmaxPa: 1000, MeasurementSeconds: seconds, Direction: "pressure"}}
}

func waitFor(t *testing.T, engine *Engine, predicate func(State) bool) State {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		state := engine.State()
		if predicate(state) {
			return state
		}
		select {
		case <-engine.Changes():
		case <-timer.C:
			t.Fatalf("state did not arrive: %+v", state)
		}
	}
}

func TestSimulatorRunPersistsMeasurementsAndReopens(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	engine := newTestEngine(t, store, NewSimulator(), uuid.New())
	created, err := engine.Create(ctx, command(0.05))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %+v %v", created, err)
	}
	id := *created.TestId
	wrong := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := engine.Start(ctx, uuid.New(), wrong); !errors.Is(err, storage.ErrTestNotFound) {
		t.Fatalf("start for unknown test: %v", err)
	}
	if _, err := engine.Start(ctx, id, wrong); !errors.Is(err, storage.ErrCommandConflict) {
		t.Fatalf("reused rejected command: %v", err)
	}
	readiness, err := engine.Readiness(id)
	if err != nil || !readiness.Ready || readiness.Revision != 1 || len(readiness.Checks) != 2 {
		t.Fatalf("readiness: %+v %v", readiness, err)
	}
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	status, err := engine.Start(ctx, id, start)
	if err != nil || status.Status != "accepted" {
		t.Fatalf("start: %+v %v", status, err)
	}
	state := engine.State()
	if state.PendingAction == nil || state.Record == nil || len(state.Measurements) != 0 {
		t.Fatalf("pending action: %+v", state)
	}
	confirm := contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}
	if _, err := engine.Confirm(ctx, id, state.PendingAction.ActionId, confirm); err != nil {
		t.Fatal(err)
	}
	rejected := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 4, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := engine.Start(ctx, id, rejected); !errors.Is(err, storage.ErrInvalidState) && !errors.Is(err, storage.ErrRevision) {
		t.Fatalf("start while running: %v", err)
	}
	result := waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "saved"
	})
	if result.Record.Outcome != "completed" || result.Record.DataQuality != "complete" {
		t.Fatalf("result: %+v", result.Record)
	}
	if replay, err := engine.Start(ctx, id, start); err != nil || replay.Status != "completed" {
		t.Fatalf("start replay: %+v %v", replay, err)
	}
	if replay, err := engine.Confirm(ctx, id, state.PendingAction.ActionId, confirm); err != nil || replay.Status != "completed" {
		t.Fatalf("confirmation replay: %+v %v", replay, err)
	}
	changed := confirm
	changed.ExpectedRevision++
	if _, err := engine.Confirm(ctx, id, state.PendingAction.ActionId, changed); !errors.Is(err, storage.ErrCommandConflict) {
		t.Fatalf("changed confirmation: %v", err)
	}
	rejected.ExpectedRevision++
	if _, err := engine.Start(ctx, id, rejected); !errors.Is(err, storage.ErrCommandConflict) {
		t.Fatalf("changed rejected start: %v", err)
	}
	samples, err := store.Measurements(ctx, id, 0, 100)
	if err != nil || len(samples) < 6 {
		t.Fatalf("measurements: %+v %v", samples, err)
	}
	if samples[0].Channel != "dd1" || samples[1].Channel != "dd2" || samples[2].Channel != "dd3" || samples[0].Source != "simulator" || samples[0].Unit != "Pa" || samples[0].Value == nil || samples[3].Value == nil || *samples[0].Value == *samples[3].Value {
		t.Fatalf("simulated channels: %+v", samples)
	}
	next := command(0.05)
	next.ExpectedRevision = result.Revision
	if _, err := engine.Create(ctx, next); err != nil {
		t.Fatalf("create after completion using snapshot revision %d: %v", result.Revision, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	reopened, err := store.Test(ctx, id)
	if err != nil || reopened.Outcome != "completed" || reopened.SaveStatus != "saved" {
		t.Fatalf("reopened: %+v %v", reopened, err)
	}
	samples, err = store.Measurements(ctx, id, 0, 100)
	if err != nil || len(samples) < 6 {
		t.Fatalf("reopened samples: %d %v", len(samples), err)
	}
}

type stalledStart struct {
	*storage.Store
	entered chan struct{}
	release chan struct{}
}

type blockedRejection struct {
	*storage.Store
	entered chan struct{}
	result  string
}

type controlledDeadline struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func (d *controlledDeadline) Done() <-chan struct{} { return d.done }
func (d *controlledDeadline) Err() error {
	select {
	case <-d.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
func (d *controlledDeadline) expire() { d.once.Do(func() { close(d.done) }) }

func waitForRejectionEntry(t *testing.T, entered <-chan struct{}, result <-chan error) {
	t.Helper()
	select {
	case <-entered:
	case err := <-result:
		t.Fatalf("request ended before rejection: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("rejection was not reached")
	}
}

func rejectionResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("rejection did not return")
		return nil
	}
}

func (s *blockedRejection) wait(ctx context.Context) (contract.CommandStatus, error) {
	close(s.entered)
	<-ctx.Done()
	switch s.result {
	case "not_started":
		return contract.CommandStatus{}, fmt.Errorf("%w: %w", storage.ErrWriteNotStarted, ctx.Err())
	case "storage_failure":
		return contract.CommandStatus{}, storage.ErrStorageUnavailable
	default:
		return contract.CommandStatus{}, ctx.Err()
	}
}

func (s *blockedRejection) RejectStartUnavailable(ctx context.Context, _ uuid.UUID, _ contract.StartTestCommand) (contract.CommandStatus, error) {
	return s.wait(ctx)
}

func (s *blockedRejection) RejectConfirmUnavailable(ctx context.Context, _, _ uuid.UUID, _ contract.ConfirmActionCommand) (contract.CommandStatus, error) {
	return s.wait(ctx)
}

func TestOtherTestRejectionDeadlineDoesNotStopActiveRun(t *testing.T) {
	for _, operation := range []string{"start", "confirm"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			store, err := storage.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			blocked := &blockedRejection{Store: store, entered: make(chan struct{}), result: "not_started"}
			simulator := &countedSimulator{}
			engine := newTestEngine(t, blocked, simulator, uuid.New())
			created, err := engine.Create(ctx, command(20))
			if err != nil || created.TestId == nil {
				t.Fatalf("create: %v", err)
			}
			id := *created.TestId
			if _, err := engine.Start(ctx, id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
				t.Fatal(err)
			}
			action := engine.State().PendingAction
			if action == nil {
				t.Fatal("missing action")
			}
			if _, err := engine.Confirm(ctx, id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}); err != nil {
				t.Fatal(err)
			}
			initial := waitFor(t, engine, func(state State) bool { return len(state.Measurements) > 0 })
			deadline := &controlledDeadline{Context: ctx, done: make(chan struct{})}
			defer deadline.expire()
			done := make(chan error, 1)
			go func() {
				if operation == "start" {
					_, err := engine.Start(deadline, uuid.New(), contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 4, ValveClosed: true, ConnectionConfirmed: true})
					done <- err
				} else {
					_, err := engine.Confirm(deadline, uuid.New(), uuid.New(), contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 4})
					done <- err
				}
			}()
			waitForRejectionEntry(t, blocked.entered, done)
			deadline.expire()
			if err := rejectionResult(t, done); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("rejection: %v", err)
			}
			continued := waitFor(t, engine, func(state State) bool {
				return len(state.Measurements) > 0 && state.Measurements[0].Sequence > initial.Measurements[0].Sequence
			})
			if continued.Record == nil || continued.Record.Stage != "running" || continued.Stop != nil || simulator.stops.Load() != 0 {
				t.Fatalf("active run changed: %+v", continued)
			}
			if _, err := engine.Stop(ctx, id, continued.Record.StopCommandId); err != nil {
				t.Fatal(err)
			}
			waitFor(t, engine, func(state State) bool {
				return state.Record != nil && state.Record.SaveStatus == "saved" && state.Record.Stage == "finished"
			})
		})
	}
}

func TestOtherTestRejectionFailureStillStopsActiveRun(t *testing.T) {
	for _, failure := range []string{"storage_failure", "late_deadline"} {
		t.Run(failure, func(t *testing.T) {
			store, err := storage.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			blocked := &blockedRejection{Store: store, entered: make(chan struct{}), result: failure}
			simulator := &countedSimulator{}
			engine := newTestEngine(t, blocked, simulator, uuid.New())
			created, err := engine.Create(context.Background(), command(20))
			if err != nil || created.TestId == nil {
				t.Fatalf("create: %v", err)
			}
			id := *created.TestId
			if _, err := engine.Start(context.Background(), id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
				t.Fatal(err)
			}
			action := engine.State().PendingAction
			if action == nil {
				t.Fatal("missing action")
			}
			if _, err := engine.Confirm(context.Background(), id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, engine, func(state State) bool { return len(state.Measurements) > 0 })
			ctx := &controlledDeadline{Context: context.Background(), done: make(chan struct{})}
			defer ctx.expire()
			done := make(chan error, 1)
			go func() {
				_, err := engine.Start(ctx, uuid.New(), contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true})
				done <- err
			}()
			waitForRejectionEntry(t, blocked.entered, done)
			ctx.expire()
			err = rejectionResult(t, done)
			if failure == "storage_failure" && !errors.Is(err, storage.ErrStorageUnavailable) {
				t.Fatalf("storage failure: %v", err)
			}
			if failure == "late_deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("late deadline: %v", err)
			}
			state := waitFor(t, engine, func(state State) bool { return state.Stop != nil && state.Stop.Status == "completed" })
			if state.Record == nil || state.Record.Outcome != "interrupted" || simulator.stops.Load() != 1 {
				t.Fatalf("mandatory failure: %+v", state)
			}
			waitFor(t, engine, func(state State) bool {
				return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "saved"
			})
		})
	}
}

type timedOutLookup struct{ *storage.Store }

func (*timedOutLookup) LookupStart(ctx context.Context, _ uuid.UUID, _ contract.StartTestCommand) (contract.CommandStatus, error) {
	<-ctx.Done()
	return contract.CommandStatus{}, ctx.Err()
}

func TestClientLookupDeadlineDoesNotStopSimulator(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	simulator := &countedSimulator{}
	engine := newTestEngine(t, &timedOutLookup{Store: store}, simulator, uuid.New())
	created, err := engine.Create(context.Background(), command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = engine.Start(ctx, *created.TestId, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lookup: %v", err)
	}
	state := engine.State()
	if state.Record == nil || state.Record.Stage != "preparing" || state.Stop != nil || simulator.stops.Load() != 0 {
		t.Fatalf("deadline changed run: %+v", state)
	}
}

type canceledResponse struct {
	*storage.Store
	cancel context.CancelFunc
}

func (s *canceledResponse) Start(ctx context.Context, id uuid.UUID, cmd contract.StartTestCommand, action contract.PendingAction) (contract.CommandStatus, error) {
	status, err := s.Store.Start(ctx, id, cmd, action)
	s.cancel()
	return status, err
}

func (s *canceledResponse) Confirm(ctx context.Context, id, actionID uuid.UUID, cmd contract.ConfirmActionCommand) (contract.CommandStatus, error) {
	status, err := s.Store.Confirm(ctx, id, actionID, cmd)
	s.cancel()
	return status, err
}

func TestCommittedCommandsContinueAfterClientCancellation(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	wrapped := &canceledResponse{Store: store}
	engine := newTestEngine(t, wrapped, NewSimulator(), uuid.New())
	created, err := engine.Create(context.Background(), command(0.05))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	ctx, cancel := context.WithCancel(context.Background())
	wrapped.cancel = cancel
	_, err = engine.Start(ctx, id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true})
	if err != nil || engine.State().PendingAction == nil {
		t.Fatalf("start after cancellation: %v", err)
	}
	actionID := engine.State().PendingAction.ActionId
	ctx, cancel = context.WithCancel(context.Background())
	wrapped.cancel = cancel
	_, err = engine.Confirm(ctx, id, actionID, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2})
	if err != nil {
		t.Fatalf("confirm after cancellation: %v", err)
	}
	waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "saved"
	})
}

type failedCreateRead struct {
	*storage.Store
	fail bool
}

type lostCreateResponse struct{ *storage.Store }

type lostExactCreateResponse struct{ *storage.Store }

func (s *lostExactCreateResponse) CreateExact(ctx context.Context, command contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	_, err := s.Store.CreateExact(ctx, command, identity)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	return contract.CommandStatus{}, context.DeadlineExceeded
}

func TestRefreshPreservesExactCreateIdentityAfterLostResponse(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine := newTestEngine(t, &lostExactCreateResponse{Store: store}, NewSimulator(), uuid.New())
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"V","areaM2":1.0000000000000001,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	var command contract.CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", []byte(body), &command)
	if err != nil || !input.Valid {
		t.Fatalf("input: %v", err)
	}
	if _, err := engine.CreateExact(context.Background(), command, input.Identity); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("create: %v", err)
	}
	if err := engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state := engine.State(); state.Record == nil || state.Record.Stage != "preparing" {
		t.Fatalf("refresh: %+v", state)
	}
	var other contract.CreateTestCommand
	changed, err := validator.Inspect("CreateTestCommand", []byte(strings.Replace(body, `1.0000000000000001`, `1`, 1)), &other)
	if err != nil || command != other {
		t.Fatalf("changed input: %v", err)
	}
	if _, err := engine.CreateExact(context.Background(), other, changed.Identity); !errors.Is(err, storage.ErrCommandConflict) {
		t.Fatalf("conflict: %v", err)
	}
}

func (s *lostCreateResponse) Create(ctx context.Context, command contract.CreateTestCommand) (contract.CommandStatus, error) {
	_, err := s.Store.Create(ctx, command)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	return contract.CommandStatus{}, context.DeadlineExceeded
}

func TestRefreshFindsCreatedTestAfterLostCommitResponse(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine := newTestEngine(t, &lostCreateResponse{Store: store}, NewSimulator(), uuid.New())
	if _, err := engine.Create(context.Background(), command(1)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("create: %v", err)
	}
	if err := engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state := engine.State(); state.Record == nil || state.Record.Stage != "preparing" || state.Revision != 1 {
		t.Fatalf("refreshed: %+v", state)
	}
}

func (s *failedCreateRead) Test(ctx context.Context, id uuid.UUID) (contract.TestRecord, error) {
	if s.fail {
		s.fail = false
		return contract.TestRecord{}, context.DeadlineExceeded
	}
	return s.Store.Test(ctx, id)
}

func TestRefreshFindsCreatedTestAfterLostRead(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	wrapped := &failedCreateRead{Store: store, fail: true}
	engine := newTestEngine(t, wrapped, NewSimulator(), uuid.New())
	created, err := engine.Create(context.Background(), command(1))
	if !errors.Is(err, context.DeadlineExceeded) || created.TestId == nil {
		t.Fatalf("expected uncertain response: %+v %v", created, err)
	}
	if err := engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := engine.State()
	if state.Record == nil || state.Record.TestId != *created.TestId || state.Revision != 1 {
		t.Fatalf("refreshed state: %+v", state)
	}
}

func (s *stalledStart) Start(ctx context.Context, id uuid.UUID, cmd contract.StartTestCommand, action contract.PendingAction) (contract.CommandStatus, error) {
	result, err := s.Store.Start(ctx, id, cmd, action)
	close(s.entered)
	<-s.release
	return result, err
}

type countedSimulator struct{ starts, stops, finishes atomic.Int32 }

type failingStopSimulator struct{ countedSimulator }

func (s *failingStopSimulator) Stop() error {
	s.stops.Add(1)
	return errors.New("simulator stop failed")
}

func TestFailedStopIsAuditedAndDoesNotReleaseTest(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	simulator := &failingStopSimulator{}
	engine := newTestEngine(t, store, simulator, uuid.New())
	events, _ := engine.CommandEvents()
	created, err := engine.Create(ctx, command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	stopID := engine.State().Record.StopCommandId
	if _, err := engine.Stop(ctx, id, stopID); err != nil {
		t.Fatal(err)
	}
	state := waitFor(t, engine, func(state State) bool {
		return state.Stop != nil && state.Stop.Status == "failed" && state.Record.SaveStatus == "saved"
	})
	if state.Record.Outcome != "unknown" {
		t.Fatalf("failed stop outcome: %+v", state.Record)
	}
	if state.Stop.Reason == nil || *state.Stop.Reason != "simulator_stop_failed" {
		t.Fatalf("missing stop failure reason: %+v", state.Stop)
	}
	foundFailure := false
	for len(events) > 0 {
		event := <-events
		if event.Status.Status == "failed" && event.Status.Reason != nil && *event.Status.Reason == "simulator_stop_failed" {
			foundFailure = true
		}
	}
	if !foundFailure {
		t.Fatal("stop failure was not reported to diagnostics")
	}
	status, err := store.LookupStop(ctx, id, stopID)
	if err != nil || status.Status != "failed" {
		t.Fatalf("stored stop: %+v %v", status, err)
	}
	next := command(1)
	next.ExpectedRevision = state.Revision
	if _, err := engine.Create(ctx, next); !errors.Is(err, storage.ErrActiveTest) {
		t.Fatalf("new test after failed stop: %v", err)
	}
}

func (*countedSimulator) Ready() bool           { return true }
func (s *countedSimulator) Start(float64) error { s.starts.Add(1); return nil }
func (*countedSimulator) Observe(time.Duration) (float64, float64, float64, error) {
	return 1, 2, 3, nil
}
func (s *countedSimulator) Stop() error   { s.stops.Add(1); return nil }
func (s *countedSimulator) Finish() error { s.finishes.Add(1); return nil }

func TestStopDoesNotWaitForDelayedStartAndPreventsLateStart(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	delayed := &stalledStart{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	simulator := &countedSimulator{}
	engine := newTestEngine(t, delayed, simulator, uuid.New())
	created, err := engine.Create(ctx, command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	initial := engine.State()
	if initial.Record == nil {
		t.Fatal("missing active record")
	}
	startDone := make(chan struct{})
	go func() {
		_, _ = engine.Start(ctx, id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true})
		close(startDone)
	}()
	<-delayed.entered
	status, err := engine.Stop(ctx, id, initial.Record.StopCommandId)
	if err != nil || status.Status != "accepted" {
		t.Fatalf("stop acceptance: %+v %v", status, err)
	}
	waitFor(t, engine, func(state State) bool { return state.Stop != nil && state.Stop.Status == "completed" })
	if simulator.stops.Load() != 1 {
		t.Fatalf("stop calls: %d", simulator.stops.Load())
	}
	if _, err := engine.Stop(ctx, id, initial.Record.StopCommandId); err != nil {
		t.Fatalf("stop replay: %v", err)
	}
	close(delayed.release)
	<-startDone
	if simulator.starts.Load() != 0 || simulator.stops.Load() != 1 {
		t.Fatalf("late or repeated effect: starts=%d stops=%d", simulator.starts.Load(), simulator.stops.Load())
	}
	waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "saved"
	})
}

type blockedSamples struct {
	*storage.Store
	gate    sync.Mutex
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func TestNaturalCompletionStopsWhenMeasurementWriteStalls(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	blocked := &blockedSamples{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(blocked.release)
		}
	}()
	simulator := &countedSimulator{}
	engine := newTestEngine(t, blocked, simulator, uuid.New())
	created, err := engine.Create(ctx, command(0.05))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	if _, err := engine.Start(ctx, id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	action := engine.State().PendingAction
	if action == nil {
		t.Fatal("missing action")
	}
	if _, err := engine.Confirm(ctx, id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("measurement writer did not start")
	}
	stopped := waitFor(t, engine, func(state State) bool { return state.Stop != nil && state.Stop.Status == "completed" })
	if stopped.Record == nil || stopped.Record.Outcome != "interrupted" || stopped.Record.DataQuality != "incomplete" || stopped.Record.SaveStatus == "saved" || simulator.stops.Load() != 1 || simulator.finishes.Load() != 0 {
		t.Fatalf("system stop: %+v", stopped)
	}
	unknown := waitFor(t, engine, func(state State) bool { return state.Record != nil && state.Record.SaveStatus == "unknown" })
	if unknown.Record.Outcome != "interrupted" {
		t.Fatalf("unknown save: %+v", unknown.Record)
	}
	close(blocked.release)
	released = true
	result := waitFor(t, engine, func(state State) bool { return state.Record != nil && state.Record.SaveStatus == "saved" })
	if simulator.stops.Load() != 1 || result.Record.Outcome != "interrupted" {
		t.Fatalf("saved stop: %+v", result.Record)
	}
	samples, err := store.Measurements(ctx, id, 0, 100)
	if err != nil || len(samples) < 6 {
		t.Fatalf("drained samples: %d %v", len(samples), err)
	}
	for index, sample := range samples {
		if sample.Sequence != int64(index+1) {
			t.Fatalf("sample gap: %+v", samples)
		}
	}
	audit, err := store.Audit(ctx, id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range audit {
		if event.Action == "stop" && event.Outcome == "accepted" && event.OperatorId == "system" && event.Reason != nil && *event.Reason == "measurement_write_timeout" {
			found = true
		}
	}
	if !found {
		t.Fatalf("system timeout audit: %+v", audit)
	}
	reopened := newTestEngine(t, store, simulator, uuid.New())
	if _, err := reopened.Stop(ctx, id, result.Record.StopCommandId); err != nil || simulator.stops.Load() != 1 {
		t.Fatalf("replayed stop: %v", err)
	}
}

func (s *blockedSamples) RecordMeasurements(ctx context.Context, id uuid.UUID, samples []storage.TimedMeasurement) error {
	s.gate.Lock()
	defer s.gate.Unlock()
	s.once.Do(func() { close(s.entered); <-s.release })
	return s.Store.RecordMeasurements(ctx, id, samples)
}
func (s *blockedSamples) FinishStop(ctx context.Context, testID, commandID uuid.UUID, details storage.StopDetails) (contract.CommandStatus, error) {
	s.gate.Lock()
	defer s.gate.Unlock()
	return s.Store.FinishStop(ctx, testID, commandID, details)
}

func TestStopBypassesBlockedMeasurementStorage(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	blocked := &blockedSamples{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	simulator := &countedSimulator{}
	engine := newTestEngine(t, blocked, simulator, uuid.New())
	created, err := engine.Create(ctx, command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := engine.Start(ctx, id, start); err != nil {
		t.Fatal(err)
	}
	state := engine.State()
	if state.PendingAction == nil || state.Record == nil {
		t.Fatal("missing pending action")
	}
	if _, err := engine.Confirm(ctx, id, state.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	<-blocked.entered
	waitFor(t, engine, func(state State) bool { return len(state.Measurements) > 0 && state.Measurements[0].Sequence >= 4 })
	if _, err := engine.Stop(ctx, id, state.Record.StopCommandId); err != nil {
		t.Fatal(err)
	}
	stopped := waitFor(t, engine, func(state State) bool { return state.Stop != nil && state.Stop.Status == "completed" })
	if simulator.stops.Load() != 1 || stopped.Record == nil || stopped.Record.SaveStatus != "pending" || stopped.Record.Outcome != "interrupted" {
		t.Fatalf("stop while blocked: %+v", stopped)
	}
	if _, err := engine.Stop(ctx, id, state.Record.StopCommandId); err != nil || simulator.stops.Load() != 1 {
		t.Fatalf("duplicate stop: %v", err)
	}
	waitFor(t, engine, func(state State) bool { return state.Record != nil && state.Record.SaveStatus == "unknown" })
	close(blocked.release)
	waitFor(t, engine, func(state State) bool { return state.Record != nil && state.Record.SaveStatus == "saved" })
	samples, err := store.Measurements(ctx, id, 0, 100)
	if err != nil || len(samples) < 6 {
		t.Fatalf("queued measurements after stop: %d %v", len(samples), err)
	}
	reopenedEngine := newTestEngine(t, store, simulator, uuid.New())
	replayed, err := reopenedEngine.Stop(ctx, id, state.Record.StopCommandId)
	if err != nil || replayed.Status != "completed" || simulator.stops.Load() != 1 {
		t.Fatalf("replayed stop after restart: %+v %v", replayed, err)
	}
	afterStop := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 4, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := engine.Start(ctx, id, afterStop); !errors.Is(err, storage.ErrInvalidState) && !errors.Is(err, storage.ErrRevision) {
		t.Fatalf("start after stop: %v", err)
	}
	afterStop.ExpectedRevision++
	if _, err := engine.Start(ctx, id, afterStop); !errors.Is(err, storage.ErrCommandConflict) {
		t.Fatalf("changed start after stop: %v", err)
	}
}

type failingSamples struct{ *storage.Store }

type invalidSamples struct{ *storage.Store }

type delayedStopCommit struct {
	*storage.Store
	calls atomic.Int32
}

type delayedCompleteCommit struct {
	*storage.Store
	calls atomic.Int32
}

func (s *delayedCompleteCommit) Complete(ctx context.Context, id uuid.UUID, elapsed time.Duration) error {
	if s.calls.Add(1) == 1 {
		return context.DeadlineExceeded
	}
	return s.Store.Complete(ctx, id, elapsed)
}

func TestRefreshPersistsCompletedRunAfterWriteDeadline(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine := newTestEngine(t, &delayedCompleteCommit{Store: store}, NewSimulator(), uuid.New())
	create := command(0.05)
	created, err := engine.Create(ctx, create)
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	_, err = engine.Start(ctx, id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	action := engine.State().PendingAction
	if action == nil {
		t.Fatal("missing action")
	}
	_, err = engine.Confirm(ctx, id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, engine, func(state State) bool { return state.Record != nil && state.Record.SaveStatus == "unknown" })
	if replay, err := engine.Create(ctx, create); err != nil || replay.TestId == nil || *replay.TestId != id {
		t.Fatalf("create replay: %+v %v", replay, err)
	}
	changed := create
	changed.Parameters.Valve = "другая"
	if _, err := engine.Create(ctx, changed); !errors.Is(err, storage.ErrCommandConflict) {
		t.Fatalf("create collision: %v", err)
	}
	if err := engine.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	state := engine.State()
	if state.Record.SaveStatus != "saved" || state.Record.Outcome != "completed" {
		t.Fatalf("refreshed completion: %+v", state.Record)
	}
}

func (s *delayedStopCommit) FinishStop(ctx context.Context, testID, commandID uuid.UUID, details storage.StopDetails) (contract.CommandStatus, error) {
	if s.calls.Add(1) == 1 {
		return contract.CommandStatus{}, context.DeadlineExceeded
	}
	return s.Store.FinishStop(ctx, testID, commandID, details)
}

func TestRefreshPersistsConfirmedStopAfterWriteDeadline(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	simulator := &countedSimulator{}
	engine := newTestEngine(t, &delayedStopCommit{Store: store}, simulator, uuid.New())
	created, err := engine.Create(ctx, command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	stopID := engine.State().Record.StopCommandId
	if _, err := engine.Stop(ctx, id, stopID); err != nil {
		t.Fatal(err)
	}
	state := waitFor(t, engine, func(state State) bool { return state.Record != nil && state.Record.SaveStatus == "unknown" })
	if err := waitForStop(ctx, engine.active.stopDone); err != nil {
		t.Fatal(err)
	}
	next := command(1)
	next.ExpectedRevision = state.Revision
	if _, err := engine.Create(ctx, next); !errors.Is(err, ErrBusy) {
		t.Fatalf("create during uncertain stop: %v", err)
	}
	if err := engine.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	state = engine.State()
	if state.Record.SaveStatus != "saved" || state.Record.Outcome != "interrupted" || simulator.stops.Load() != 1 {
		t.Fatalf("refreshed stop: %+v", state)
	}
	next.ExpectedRevision = state.Revision
	if _, err := engine.Create(ctx, next); err != nil {
		t.Fatalf("create after refresh: %v", err)
	}
}

func (*invalidSamples) RecordMeasurements(context.Context, uuid.UUID, []storage.TimedMeasurement) error {
	return storage.ErrInvalidCommand
}

func TestInvalidMeasurementRecordsSystemStop(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine := newTestEngine(t, &invalidSamples{Store: store}, &countedSimulator{}, uuid.New())
	created, err := engine.Create(ctx, command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	_, err = engine.Start(ctx, id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	action := engine.State().PendingAction
	if action == nil {
		t.Fatal("missing action")
	}
	_, err = engine.Confirm(ctx, id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	result := waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "failed"
	})
	audit, err := store.Audit(ctx, id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range audit {
		if event.Action == "stop" && event.Outcome == "accepted" && event.OperatorId == "system" && event.Reason != nil && *event.Reason == "measurement_write_failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing system stop reason: %+v", audit)
	}
	late := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: result.Revision, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := engine.Start(ctx, id, late); !errors.Is(err, storage.ErrInvalidState) {
		t.Fatalf("late start: %v", err)
	}
	next := command(1)
	next.ExpectedRevision = result.Revision
	newTest, err := engine.Create(ctx, next)
	if err != nil || newTest.TestId == nil {
		t.Fatalf("new test after persisted incomplete result: %v", err)
	}
	late.ExpectedRevision++
	if _, err := engine.Start(ctx, *newTest.TestId, late); !errors.Is(err, storage.ErrCommandConflict) {
		t.Fatalf("reused rejected command: %v", err)
	}
}

func (*failingSamples) RecordMeasurements(context.Context, uuid.UUID, []storage.TimedMeasurement) error {
	return storage.ErrStorageUnavailable
}
func (*failingSamples) FinishStop(context.Context, uuid.UUID, uuid.UUID, storage.StopDetails) (contract.CommandStatus, error) {
	return contract.CommandStatus{}, storage.ErrStorageUnavailable
}

func TestStorageFailureStopsSimulatorWithoutClaimingSaved(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	simulator := &countedSimulator{}
	engine := newTestEngine(t, &failingSamples{Store: store}, simulator, uuid.New())
	created, err := engine.Create(ctx, command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := engine.Start(ctx, id, start); err != nil {
		t.Fatal(err)
	}
	state := engine.State()
	if state.PendingAction == nil {
		t.Fatal("missing action")
	}
	if _, err := engine.Confirm(ctx, id, state.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	result := waitFor(t, engine, func(state State) bool { return state.Record != nil && state.Record.SaveStatus == "failed" })
	if result.Stop == nil || result.Stop.Status != "completed" || result.Record.Outcome != "interrupted" || result.Record.DataQuality != "incomplete" || simulator.stops.Load() != 1 {
		t.Fatalf("failure state: %+v", result)
	}
}

type unavailableSimulator struct{ countedSimulator }

func (*unavailableSimulator) Ready() bool { return false }

func TestNotReadyStartIsRejectedAndKeepsCommandIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine := newTestEngine(t, store, &unavailableSimulator{}, uuid.New())
	created, err := engine.Create(ctx, command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	readiness, err := engine.Readiness(id)
	if err != nil || readiness.Ready {
		t.Fatalf("readiness: %+v %v", readiness, err)
	}
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	if status, err := engine.Start(ctx, id, start); !errors.Is(err, storage.ErrNotReady) || status.Status != "rejected" {
		t.Fatalf("not ready: %+v %v", status, err)
	}
	changed := start
	changed.ExpectedRevision++
	if _, err := engine.Start(ctx, id, changed); !errors.Is(err, storage.ErrCommandConflict) {
		t.Fatalf("changed rejected command: %v", err)
	}
}

func TestWrongConfirmationDoesNotStartSimulator(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	simulator := &countedSimulator{}
	engine := newTestEngine(t, store, simulator, uuid.New())
	created, err := engine.Create(ctx, command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := engine.Start(ctx, id, start); err != nil {
		t.Fatal(err)
	}
	state := engine.State()
	if state.PendingAction == nil || state.Record == nil {
		t.Fatal("missing action")
	}
	wrong := contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}
	if _, err := engine.Confirm(ctx, id, uuid.New(), wrong); !errors.Is(err, storage.ErrActionMismatch) {
		t.Fatalf("wrong action: %v", err)
	}
	if simulator.starts.Load() != 0 {
		t.Fatal("wrong confirmation started simulator")
	}
	if _, err := engine.Confirm(ctx, id, state.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	if simulator.starts.Load() != 1 {
		t.Fatalf("start count: %d", simulator.starts.Load())
	}
	if _, err := engine.Stop(ctx, id, state.Record.StopCommandId); err != nil {
		t.Fatal(err)
	}
	waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.SaveStatus == "saved" && state.Record.Stage == "finished"
	})
}

type stalledConfirm struct {
	*storage.Store
	entered chan struct{}
	release chan struct{}
}

func (s *stalledConfirm) Confirm(ctx context.Context, testID, actionID uuid.UUID, cmd contract.ConfirmActionCommand) (contract.CommandStatus, error) {
	result, err := s.Store.Confirm(ctx, testID, actionID, cmd)
	close(s.entered)
	<-s.release
	return result, err
}

func TestStopPreventsSimulatorStartAfterDelayedConfirmation(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	delayed := &stalledConfirm{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	simulator := &countedSimulator{}
	engine := newTestEngine(t, delayed, simulator, uuid.New())
	created, err := engine.Create(ctx, command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := engine.Start(ctx, id, start); err != nil {
		t.Fatal(err)
	}
	state := engine.State()
	if state.PendingAction == nil || state.Record == nil {
		t.Fatal("missing action")
	}
	confirmDone := make(chan struct{})
	go func() {
		_, _ = engine.Confirm(ctx, id, state.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2})
		close(confirmDone)
	}()
	<-delayed.entered
	if _, err := engine.Stop(ctx, id, state.Record.StopCommandId); err != nil {
		t.Fatal(err)
	}
	waitFor(t, engine, func(state State) bool { return state.Stop != nil && state.Stop.Status == "completed" })
	close(delayed.release)
	<-confirmDone
	if simulator.starts.Load() != 0 || simulator.stops.Load() != 1 {
		t.Fatalf("late action: starts=%d stops=%d", simulator.starts.Load(), simulator.stops.Load())
	}
	waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.SaveStatus == "saved" && state.Record.Stage == "finished"
	})
}

type heldExactCreate struct {
	*storage.Store
	entered chan struct{}
	release chan struct{}
}

func (s *heldExactCreate) CreateExact(ctx context.Context, cmd contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	result, err := s.Store.CreateExact(ctx, cmd, identity)
	close(s.entered)
	<-s.release
	return result, err
}

func TestRevocationStopsCreateCommittedBeforeResponse(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	held := &heldExactCreate{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	sim := &countedSimulator{}
	engine := newTestEngine(t, held, sim, uuid.New())
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"V","areaM2":1.0000000000000001,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	var cmd contract.CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", []byte(body), &cmd)
	if err != nil || !input.Valid {
		t.Fatalf("input: %v", err)
	}
	done := make(chan struct{})
	go func() { _, _ = controller.CreateExact(context.Background(), cmd, input.Identity); close(done) }()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("create did not enter")
	}
	controller.Revoke("ui_lost")
	next, err := engine.GrantControl()
	if err != nil {
		t.Fatalf("new client cannot observe pending create: %v", err)
	}
	if _, err := next.Create(context.Background(), command(1)); !errors.Is(err, ErrBusy) {
		t.Fatalf("pending create admitted another action: %v", err)
	}
	close(held.release)
	<-done
	state := waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "saved"
	})
	if state.Record.Outcome != "interrupted" || sim.stops.Load() != 1 || sim.starts.Load() != 0 {
		t.Fatalf("stop: %+v", state)
	}
	audit, err := store.Audit(context.Background(), state.Record.TestId, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range audit {
		if event.Action == "stop" && event.Outcome == "accepted" && event.OperatorId == "system" && event.Reason != nil && *event.Reason == "ui_lost" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing system stop audit: %+v", audit)
	}
	if _, err := controller.Start(context.Background(), state.Record.TestId, contract.StartTestCommand{CommandId: uuid.New()}); !errors.Is(err, ErrControlRevoked) {
		t.Fatalf("stale control: %v", err)
	}
}

func TestRevocationResolvesCreateAfterUnknownResponse(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	sim := &countedSimulator{}
	engine := newTestEngine(t, &lostExactCreateResponse{Store: store}, sim, uuid.New())
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"V","areaM2":1.0000000000000001,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	var cmd contract.CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", []byte(body), &cmd)
	if err != nil || !input.Valid {
		t.Fatalf("input: %v", err)
	}
	if _, err := controller.CreateExact(context.Background(), cmd, input.Identity); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("create: %v", err)
	}
	controller.Revoke("ui_lost")
	state := waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "saved"
	})
	if state.Record.Outcome != "interrupted" || sim.stops.Load() != 1 {
		t.Fatalf("stop: %+v", state)
	}
}

func TestRevocationPreventsLateConfirmation(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	delayed := &stalledConfirm{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	sim := &countedSimulator{}
	engine := newTestEngine(t, delayed, sim, uuid.New())
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	created, err := controller.Create(context.Background(), command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	_, err = controller.Start(context.Background(), id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	action := engine.State().PendingAction
	if action == nil {
		t.Fatal("missing action")
	}
	done := make(chan struct{})
	go func() {
		_, _ = controller.Confirm(context.Background(), id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2})
		close(done)
	}()
	select {
	case <-delayed.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("confirm did not enter")
	}
	controller.Revoke("ui_lost")
	close(delayed.release)
	<-done
	waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "saved"
	})
	if sim.starts.Load() != 0 || sim.stops.Load() != 1 {
		t.Fatalf("late action: starts=%d stops=%d", sim.starts.Load(), sim.stops.Load())
	}
}

func TestShutdownStopsSimulatorWithoutWaitingForBlockedWriter(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	blocked := &blockedSamples{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	sim := &countedSimulator{}
	engine := newTestEngine(t, blocked, sim, uuid.New())
	created, err := engine.Create(context.Background(), command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	if _, err = engine.Start(context.Background(), id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	action := engine.State().PendingAction
	if action == nil {
		t.Fatal("missing action")
	}
	if _, err = engine.Confirm(context.Background(), id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not enter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err = engine.Shutdown(ctx, "owner_lost")
	cancel()
	if !errors.Is(err, ErrShutdownUnconfirmed) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown: %v", err)
	}
	if sim.stops.Load() != 1 || engine.State().Record.SaveStatus == "saved" {
		t.Fatalf("shutdown falsely confirmed: %+v", engine.State())
	}
	close(blocked.release)
	waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "saved"
	})
}

func TestShutdownReportsFailedSimulatorStop(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine := newTestEngine(t, store, &failingStopSimulator{}, uuid.New())
	if _, err := engine.Create(context.Background(), command(1)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); !errors.Is(err, ErrShutdownUnconfirmed) {
		t.Fatalf("shutdown: %v", err)
	}
	if state := engine.State(); state.Stop == nil || state.Stop.Status != "failed" || state.Record.SaveStatus != "saved" {
		t.Fatalf("failed stop and audit: %+v", state)
	}
}

type heldComplete struct {
	*storage.Store
	entered chan struct{}
	release chan struct{}
}

func (s *heldComplete) Complete(ctx context.Context, id uuid.UUID, elapsed time.Duration) error {
	close(s.entered)
	<-s.release
	return s.Store.Complete(ctx, id, elapsed)
}

func TestShutdownWaitsForNaturalCompletion(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	held := &heldComplete{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	engine := newTestEngine(t, held, &countedSimulator{}, uuid.New())
	created, err := engine.Create(context.Background(), command(0.05))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	if _, err := engine.Start(context.Background(), id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	action := engine.State().PendingAction
	if action == nil {
		t.Fatal("missing action")
	}
	if _, err := engine.Confirm(context.Background(), id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("complete did not enter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- engine.Shutdown(ctx, "owner_lost") }()
	select {
	case err := <-done:
		t.Fatalf("shutdown returned before completion: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(held.release)
	if err := <-done; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

type transientExactLookup struct {
	*lostExactCreateResponse
	fail      atomic.Bool
	calls     atomic.Int32
	attempted chan struct{}
	once      sync.Once
}

func (s *transientExactLookup) LookupCreateExact(ctx context.Context, cmd contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	s.calls.Add(1)
	if s.fail.Load() {
		s.once.Do(func() { close(s.attempted) })
		return contract.CommandStatus{}, storage.ErrStorageUnavailable
	}
	return s.Store.LookupCreateExact(ctx, cmd, identity)
}

func TestShutdownRetriesLookupUntilRecoveryOrDeadline(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	wrapped := &transientExactLookup{lostExactCreateResponse: &lostExactCreateResponse{Store: store}, attempted: make(chan struct{})}
	engine := newTestEngine(t, wrapped, &countedSimulator{}, uuid.New())
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"V","areaM2":1.0000000000000001,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	var cmd contract.CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", []byte(body), &cmd)
	if err != nil || !input.Valid {
		t.Fatalf("input: %v", err)
	}
	if _, err := engine.CreateExact(context.Background(), cmd, input.Identity); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("create: %v", err)
	}
	wrapped.fail.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- engine.Shutdown(ctx, "owner_lost") }()
	select {
	case <-wrapped.attempted:
	case <-time.After(5 * time.Second):
		t.Fatal("lookup did not fail")
	}
	wrapped.fail.Store(false)
	if err := <-done; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestRevocationResolverStopsAtShutdown(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	wrapped := &transientExactLookup{lostExactCreateResponse: &lostExactCreateResponse{Store: store}, attempted: make(chan struct{})}
	engine := newTestEngine(t, wrapped, &countedSimulator{}, uuid.New())
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"V","areaM2":1.0000000000000001,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	var cmd contract.CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", []byte(body), &cmd)
	if err != nil || !input.Valid {
		t.Fatalf("input: %v", err)
	}
	if _, err := controller.CreateExact(context.Background(), cmd, input.Identity); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("create: %v", err)
	}
	wrapped.fail.Store(true)
	controller.Revoke("ui_lost")
	select {
	case <-wrapped.attempted:
	case <-time.After(5 * time.Second):
		t.Fatal("lookup did not fail")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	err = engine.Shutdown(ctx, "owner_lost")
	cancel()
	if !errors.Is(err, ErrShutdownUnconfirmed) {
		t.Fatalf("shutdown: %v", err)
	}
	calls := wrapped.calls.Load()
	time.Sleep(600 * time.Millisecond)
	if got := wrapped.calls.Load(); got != calls {
		t.Fatalf("lookup continued after shutdown: %d -> %d", calls, got)
	}
}

func TestRevocationResolvesAfterTransientLookupFailure(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	wrapped := &transientExactLookup{lostExactCreateResponse: &lostExactCreateResponse{Store: store}, attempted: make(chan struct{})}
	engine := newTestEngine(t, wrapped, &countedSimulator{}, uuid.New())
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"V","areaM2":1.0000000000000001,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	var cmd contract.CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", []byte(body), &cmd)
	if err != nil || !input.Valid {
		t.Fatalf("input: %v", err)
	}
	if _, err := controller.CreateExact(context.Background(), cmd, input.Identity); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("create: %v", err)
	}
	wrapped.fail.Store(true)
	controller.Revoke("ui_lost")
	select {
	case <-wrapped.attempted:
	case <-time.After(5 * time.Second):
		t.Fatal("lookup did not fail")
	}
	wrapped.fail.Store(false)
	waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "saved"
	})
}

type heldExactLookup struct {
	*lostExactCreateResponse
	block   atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *heldExactLookup) LookupCreateExact(ctx context.Context, cmd contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	if s.block.Load() {
		s.once.Do(func() { close(s.entered) })
		<-s.release
	}
	return s.Store.LookupCreateExact(ctx, cmd, identity)
}

func TestShutdownDeadlineIncludesUnresponsiveCreateLookup(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	held := &heldExactLookup{lostExactCreateResponse: &lostExactCreateResponse{Store: store}, entered: make(chan struct{}), release: make(chan struct{})}
	engine := newTestEngine(t, held, &countedSimulator{}, uuid.New())
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"V","areaM2":1.0000000000000001,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	var cmd contract.CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", []byte(body), &cmd)
	if err != nil || !input.Valid {
		t.Fatalf("input: %v", err)
	}
	if _, err := engine.CreateExact(context.Background(), cmd, input.Identity); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("create: %v", err)
	}
	held.block.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- engine.Shutdown(ctx, "owner_lost") }()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("lookup did not enter")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownUnconfirmed) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown exceeded deadline")
	}
	held.block.Store(false)
	close(held.release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := engine.Refresh(context.Background())
		if err == nil {
			break
		}
		if !errors.Is(err, ErrBusy) || time.Now().After(deadline) {
			t.Fatalf("refresh after lookup returned: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "saved"
	})
}

type cancelableExactLookup struct {
	*lostExactCreateResponse
	block   atomic.Bool
	entered chan struct{}
	once    sync.Once
}

func (s *cancelableExactLookup) LookupCreateExact(ctx context.Context, cmd contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	if s.block.Swap(false) {
		s.once.Do(func() { close(s.entered) })
		<-ctx.Done()
		return contract.CommandStatus{}, ctx.Err()
	}
	return s.Store.LookupCreateExact(ctx, cmd, identity)
}

func TestShutdownCancelsRevocationLookup(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	wrapped := &cancelableExactLookup{lostExactCreateResponse: &lostExactCreateResponse{Store: store}, entered: make(chan struct{})}
	engine := newTestEngine(t, wrapped, &countedSimulator{}, uuid.New())
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"V","areaM2":1.0000000000000001,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	var cmd contract.CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", []byte(body), &cmd)
	if err != nil || !input.Valid {
		t.Fatalf("input: %v", err)
	}
	if _, err := controller.CreateExact(context.Background(), cmd, input.Identity); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("create: %v", err)
	}
	wrapped.block.Store(true)
	controller.Revoke("ui_lost")
	select {
	case <-wrapped.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("lookup did not enter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

type cancelablePostCreateLookup struct {
	*storage.Store
	calls   atomic.Int32
	entered chan struct{}
}

func (s *cancelablePostCreateLookup) LookupCreateExact(ctx context.Context, cmd contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	if s.calls.Add(1) == 2 {
		close(s.entered)
		<-ctx.Done()
		return contract.CommandStatus{}, ctx.Err()
	}
	return s.Store.LookupCreateExact(ctx, cmd, identity)
}

func TestShutdownCancelsPostCreateLookup(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	wrapped := &cancelablePostCreateLookup{Store: store, entered: make(chan struct{})}
	engine := newTestEngine(t, wrapped, &countedSimulator{}, uuid.New())
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"V","areaM2":1.0000000000000001,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	var cmd contract.CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", []byte(body), &cmd)
	if err != nil || !input.Valid {
		t.Fatalf("input: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := controller.CreateExact(context.Background(), cmd, input.Identity); done <- err }()
	select {
	case <-wrapped.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("post-create lookup did not enter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("create: %v", err)
	}
}

func TestRevokedControllerCannotIssueStop(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine := newTestEngine(t, store, &countedSimulator{}, uuid.New())
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	created, err := controller.Create(context.Background(), command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	stopID := engine.State().Record.StopCommandId
	controller.Revoke("ui_lost")
	if _, err := controller.Stop(context.Background(), *created.TestId, stopID); !errors.Is(err, ErrControlRevoked) {
		t.Fatalf("stale stop: %v", err)
	}
	waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "saved"
	})
}

type heldStopLookup struct {
	*storage.Store
	entered chan struct{}
	release chan struct{}
}

func (s *heldStopLookup) LookupStop(ctx context.Context, id, commandID uuid.UUID) (contract.CommandStatus, error) {
	close(s.entered)
	<-s.release
	return s.Store.LookupStop(ctx, id, commandID)
}

func TestStopLookupCannotReturnAfterRevocation(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	wrapped := &heldStopLookup{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	engine := newTestEngine(t, wrapped, &countedSimulator{}, uuid.New())
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := controller.Stop(context.Background(), uuid.New(), uuid.New()); done <- err }()
	select {
	case <-wrapped.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("lookup did not start")
	}
	controller.Revoke("ui_lost")
	close(wrapped.release)
	if err := <-done; !errors.Is(err, ErrControlRevoked) {
		t.Fatalf("stop lookup: %v", err)
	}
}

type heldStartLookup struct {
	*storage.Store
	block   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *heldStartLookup) LookupStart(ctx context.Context, id uuid.UUID, cmd contract.StartTestCommand) (contract.CommandStatus, error) {
	result, err := s.Store.LookupStart(ctx, id, cmd)
	if s.block.Load() {
		close(s.entered)
		<-s.release
	}
	return result, err
}

func TestStartReplayCannotReturnAfterRevocation(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	wrapped := &heldStartLookup{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	engine := newTestEngine(t, wrapped, &countedSimulator{}, uuid.New())
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	created, err := controller.Create(context.Background(), command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := controller.Start(context.Background(), *created.TestId, start); err != nil {
		t.Fatal(err)
	}
	wrapped.block.Store(true)
	done := make(chan error, 1)
	go func() { _, err := controller.Start(context.Background(), *created.TestId, start); done <- err }()
	select {
	case <-wrapped.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("lookup did not start")
	}
	controller.Revoke("ui_lost")
	close(wrapped.release)
	if err := <-done; !errors.Is(err, ErrControlRevoked) {
		t.Fatalf("replay: %v", err)
	}
	waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "saved"
	})
}

func TestRevocationCancelsUnavailableRejectionBeforeWrite(t *testing.T) {
	for _, operation := range []string{"start", "confirm"} {
		t.Run(operation, func(t *testing.T) {
			store, err := storage.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			blocked := &blockedRejection{Store: store, entered: make(chan struct{}), result: "not_started"}
			engine := newTestEngine(t, blocked, &countedSimulator{}, uuid.New())
			controller, err := engine.GrantControl()
			if err != nil {
				t.Fatal(err)
			}
			id, commandID := uuid.New(), uuid.New()
			done := make(chan error, 1)
			go func() {
				var err error
				if operation == "start" {
					_, err = controller.Start(context.Background(), id, contract.StartTestCommand{CommandId: commandID})
				} else {
					_, err = controller.Confirm(context.Background(), id, uuid.New(), contract.ConfirmActionCommand{CommandId: commandID})
				}
				done <- err
			}()
			waitForRejectionEntry(t, blocked.entered, done)
			controller.Revoke("ui_lost")
			if err := rejectionResult(t, done); !errors.Is(err, ErrControlRevoked) {
				t.Fatalf("rejection: %v", err)
			}
			if _, err := store.Command(context.Background(), commandID); !errors.Is(err, storage.ErrCommandNotFound) {
				t.Fatalf("unexpected command: %v", err)
			}
		})
	}
}

func TestControllerRejectsStartAfterConfirmedStop(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine := newTestEngine(t, store, &countedSimulator{}, uuid.New())
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	created, err := controller.Create(context.Background(), command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	stopID := engine.State().Record.StopCommandId
	if _, err := controller.Stop(context.Background(), *created.TestId, stopID); err != nil {
		t.Fatal(err)
	}
	state := waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "saved"
	})
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: state.Revision, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := controller.Start(context.Background(), *created.TestId, start); !errors.Is(err, storage.ErrInvalidState) && !errors.Is(err, storage.ErrRevision) {
		t.Fatalf("start after stop: %v", err)
	}
	status, err := store.Command(context.Background(), start.CommandId)
	if err != nil || status.Status != "rejected" {
		t.Fatalf("rejection not saved: %+v %v", status, err)
	}
}

func TestCanceledStartReplayDoesNotStartSimulator(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	sim := &countedSimulator{}
	engine := newTestEngine(t, store, sim, uuid.New())
	created, err := engine.Create(ctx, command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := engine.Start(ctx, id, start); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Stop(ctx, id, engine.State().Record.StopCommandId); err != nil {
		t.Fatal(err)
	}
	waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "saved"
	})
	status, err := engine.Start(ctx, id, start)
	if err != nil || status.Status != "failed" || status.Reason == nil || *status.Reason != "canceled_by_stop" || sim.starts.Load() != 0 || sim.stops.Load() != 1 {
		t.Fatalf("canceled replay: %+v %v; starts=%d stops=%d", status, err, sim.starts.Load(), sim.stops.Load())
	}
	changed := start
	changed.ExpectedRevision++
	if _, err := engine.Start(ctx, id, changed); !errors.Is(err, storage.ErrCommandConflict) {
		t.Fatalf("changed replay: %v", err)
	}
}

type heldUnavailableRejection struct {
	*storage.Store
	entered     chan struct{}
	release     chan struct{}
	beforeWrite chan struct{}
}

func (s *heldUnavailableRejection) RejectStartUnavailable(ctx context.Context, id uuid.UUID, cmd contract.StartTestCommand) (contract.CommandStatus, error) {
	close(s.entered)
	select {
	case <-s.release:
		if s.beforeWrite != nil {
			close(s.beforeWrite)
		}
		return s.Store.RejectStartUnavailable(ctx, id, cmd)
	case <-ctx.Done():
		return contract.CommandStatus{}, ctx.Err()
	}
}

func TestExpiredControlStopsWaitingWriteBeforeTimerRuns(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	held := &heldUnavailableRejection{Store: store, entered: make(chan struct{}), release: make(chan struct{}), beforeWrite: make(chan struct{})}
	engine := newTestEngine(t, held, &countedSimulator{}, uuid.New())
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Renew(time.Minute); err != nil {
		t.Fatal(err)
	}
	cmd := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	done := make(chan error, 1)
	go func() { _, err := controller.Start(context.Background(), uuid.New(), cmd); done <- err }()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("rejection did not start")
	}
	engine.mu.Lock()
	controller.expiresAt = time.Now().Add(-time.Second)
	close(held.release)
	select {
	case <-held.beforeWrite:
	case <-time.After(5 * time.Second):
		engine.mu.Unlock()
		t.Fatal("write was not reached")
	}
	engine.mu.Unlock()
	if err := <-done; !errors.Is(err, ErrControlRevoked) {
		t.Fatalf("expired rejection: %v", err)
	}
	if _, err := store.Command(context.Background(), cmd.CommandId); !errors.Is(err, storage.ErrCommandNotFound) {
		t.Fatalf("unexpected command: %v", err)
	}
}

type heldStartWrite struct {
	*storage.Store
	entered chan struct{}
	release chan struct{}
}

func (s *heldStartWrite) Start(ctx context.Context, id uuid.UUID, cmd contract.StartTestCommand, action contract.PendingAction) (contract.CommandStatus, error) {
	close(s.entered)
	<-s.release
	return s.Store.Start(ctx, id, cmd, action)
}

func TestExpiredControlStopsWaitingStartBeforeTimerRuns(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	held := &heldStartWrite{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	engine := newTestEngine(t, held, &countedSimulator{}, uuid.New())
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Renew(time.Minute); err != nil {
		t.Fatal(err)
	}
	created, err := controller.Create(context.Background(), command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	cmd := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	done := make(chan error, 1)
	go func() { _, err := controller.Start(context.Background(), *created.TestId, cmd); done <- err }()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("start did not reach writer")
	}
	engine.mu.Lock()
	controller.expiresAt = time.Now().Add(-time.Second)
	close(held.release)
	engine.mu.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("start: %v", err)
	}
	if _, err := store.Command(context.Background(), cmd.CommandId); !errors.Is(err, storage.ErrCommandNotFound) {
		t.Fatalf("unexpected command: %v", err)
	}
	waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "saved"
	})
}

type timedOutOperation struct {
	*storage.Store
	operation string
}

func (s *timedOutOperation) Start(ctx context.Context, id uuid.UUID, cmd contract.StartTestCommand, action contract.PendingAction) (contract.CommandStatus, error) {
	if s.operation == "start" {
		<-ctx.Done()
		return contract.CommandStatus{}, context.Canceled
	}
	return s.Store.Start(ctx, id, cmd, action)
}

func (s *timedOutOperation) Confirm(ctx context.Context, id, action uuid.UUID, cmd contract.ConfirmActionCommand) (contract.CommandStatus, error) {
	if s.operation == "confirm" {
		<-ctx.Done()
		return contract.CommandStatus{}, context.Canceled
	}
	return s.Store.Confirm(ctx, id, action, cmd)
}

func TestControllerStorageTimeoutStillStopsRun(t *testing.T) {
	for _, operation := range []string{"start", "confirm"} {
		t.Run(operation, func(t *testing.T) {
			store, err := storage.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			sim := &countedSimulator{}
			engine := newTestEngine(t, &timedOutOperation{Store: store, operation: operation}, sim, uuid.New())
			controller, err := engine.GrantControl()
			if err != nil {
				t.Fatal(err)
			}
			created, err := controller.Create(context.Background(), command(1))
			if err != nil || created.TestId == nil {
				t.Fatalf("create: %v", err)
			}
			id := *created.TestId
			_, err = controller.Start(context.Background(), id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true})
			if operation == "confirm" {
				if err != nil {
					t.Fatal(err)
				}
				action := engine.State().PendingAction
				if action == nil {
					t.Fatal("missing action")
				}
				_, err = controller.Confirm(context.Background(), id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2})
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("operation: %v", err)
			}
			state := waitFor(t, engine, func(state State) bool {
				return state.Stop != nil && state.Stop.Status == "completed" && state.Record.SaveStatus == "saved"
			})
			if state.Record.Outcome != "interrupted" || sim.stops.Load() != 1 {
				t.Fatalf("stop: %+v", state)
			}
		})
	}
}

type heldFailedExactLookup struct {
	*storage.Store
	entered chan struct{}
	release chan struct{}
}

func (s *heldFailedExactLookup) LookupCreateExact(context.Context, contract.CreateTestCommand, []byte) (contract.CommandStatus, error) {
	close(s.entered)
	<-s.release
	return contract.CommandStatus{}, storage.ErrStorageUnavailable
}

func TestLateLookupFailureDoesNotStopNewOwnerRun(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	held := &heldFailedExactLookup{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	sim := &countedSimulator{}
	engine := newTestEngine(t, held, sim, uuid.New())
	first, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	create := command(10)
	created, err := first.Create(context.Background(), create)
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	start := func(controller *Controller, id uuid.UUID) {
		t.Helper()
		revision := engine.State().Revision
		if _, err := controller.Start(context.Background(), id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: revision, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
			t.Fatal(err)
		}
		state := engine.State()
		if state.PendingAction == nil {
			t.Fatal("missing action")
		}
		if _, err := controller.Confirm(context.Background(), id, state.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: state.Revision}); err != nil {
			t.Fatal(err)
		}
	}
	start(first, *created.TestId)
	done := make(chan error, 1)
	go func() {
		_, err := first.LookupCreateExact(context.Background(), command(1), []byte("test"))
		done <- err
	}()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("lookup did not start")
	}
	first.Revoke("ui_lost")
	finished := waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "saved"
	})
	second, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	next := command(10)
	next.ExpectedRevision = finished.Revision
	nextCreated, err := second.Create(context.Background(), next)
	if err != nil || nextCreated.TestId == nil {
		t.Fatalf("new create: %v", err)
	}
	start(second, *nextCreated.TestId)
	close(held.release)
	if err := <-done; !errors.Is(err, ErrControlRevoked) {
		t.Fatalf("old lookup: %v", err)
	}
	state := engine.State()
	if state.Record == nil || state.Record.TestId != *nextCreated.TestId || state.Record.Stage != "running" || sim.starts.Load() != 2 || sim.stops.Load() != 1 {
		t.Fatalf("new run was stopped: %+v", state)
	}
	second.Revoke("ui_lost")
	waitFor(t, engine, func(state State) bool {
		return state.Record != nil && state.Record.Stage == "finished" && state.Record.SaveStatus == "saved"
	})
}

type heldCompleteStart struct {
	*storage.Store
	entered          chan struct{}
	release          chan struct{}
	commitBeforeWait bool
}

func (s *heldCompleteStart) CompleteStart(ctx context.Context, id, commandID uuid.UUID) error {
	var err error
	if s.commitBeforeWait {
		err = s.Store.CompleteStart(ctx, id, commandID)
	}
	close(s.entered)
	<-s.release
	if s.commitBeforeWait {
		return err
	}
	return s.Store.CompleteStart(ctx, id, commandID)
}

func TestStalledStartCompletionStopsSimulatorWithoutOperator(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	held := &heldCompleteStart{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	sim := &countedSimulator{}
	engine := newTestEngine(t, held, sim, uuid.New())
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	created, err := controller.Create(context.Background(), command(10))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	startID := uuid.New()
	if _, err := controller.Start(context.Background(), id, contract.StartTestCommand{CommandId: startID, ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	action := engine.State().PendingAction
	if action == nil {
		t.Fatal("missing action")
	}
	done := make(chan error, 1)
	go func() {
		_, err := controller.Confirm(context.Background(), id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2})
		done <- err
	}()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("completion did not start")
	}
	state := waitFor(t, engine, func(state State) bool { return state.Stop != nil && state.Stop.Status == "completed" })
	if state.Record.Outcome != "interrupted" || sim.starts.Load() != 1 || sim.stops.Load() != 1 {
		t.Fatalf("system stop: %+v", state)
	}
	waitFor(t, engine, func(state State) bool { return state.Record != nil && state.Record.SaveStatus == "saved" })
	startStatus, err := store.Command(context.Background(), startID)
	if err != nil || startStatus.Status != "unknown" {
		t.Fatalf("started simulator without confirmed completion: %+v %v", startStatus, err)
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	shutdownErr := engine.Shutdown(shutdownCtx, "owner_lost")
	shutdownCancel()
	if !errors.Is(shutdownErr, ErrShutdownUnconfirmed) || !errors.Is(shutdownErr, context.DeadlineExceeded) {
		t.Fatalf("shutdown while completion is stuck: %v", shutdownErr)
	}
	close(held.release)
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("completion: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatal(err)
	}
	if sim.stops.Load() != 1 {
		t.Fatalf("late completion repeated stop: %d", sim.stops.Load())
	}
}

func TestStartCompletionIsReportedAfterConcurrentStop(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	held := &heldCompleteStart{Store: store, entered: make(chan struct{}), release: make(chan struct{}), commitBeforeWait: true}
	engine := newTestEngine(t, held, &countedSimulator{}, uuid.New())
	events, _ := engine.CommandEvents()
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	created, err := controller.Create(context.Background(), command(10))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	startID := uuid.New()
	if _, err := controller.Start(context.Background(), id, contract.StartTestCommand{CommandId: startID, ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	state := engine.State()
	if state.PendingAction == nil {
		t.Fatal("missing action")
	}
	done := make(chan error, 1)
	go func() {
		_, err := controller.Confirm(context.Background(), id, state.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: state.Revision})
		done <- err
	}()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("completion did not commit")
	}
	if _, err := controller.Stop(context.Background(), id, state.Record.StopCommandId); err != nil {
		t.Fatal(err)
	}
	close(held.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatal(err)
	}
	startStatus, err := store.Command(ctx, startID)
	if err != nil || startStatus.Status != "completed" {
		t.Fatalf("confirmed start after stop: %+v %v", startStatus, err)
	}
	found := false
	for {
		select {
		case event := <-events:
			if event.Status.CommandId == startID && event.Status.Status == "completed" {
				found = true
			}
		default:
			if !found {
				t.Fatal("committed start completion not reported")
			}
			return
		}
	}
}

func TestStartCompletionDeadlineWhenBothSignalsAreReady(t *testing.T) {
	deadline := time.Now()
	for _, tc := range []struct {
		name     string
		at       time.Time
		timedOut bool
	}{
		{name: "before", at: deadline.Add(-time.Nanosecond)},
		{name: "at", at: deadline, timedOut: true},
		{name: "after", at: deadline.Add(time.Nanosecond), timedOut: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for range 100 {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				done := make(chan startCompletion, 1)
				done <- startCompletion{at: tc.at}
				calls := 0
				_, timedOut := waitStartCompletion(ctx, deadline, done, func() { calls++ })
				expectedCalls := 0
				if tc.timedOut {
					expectedCalls = 1
				}
				if timedOut != tc.timedOut || calls != expectedCalls {
					t.Fatalf("timeout=%t calls=%d", timedOut, calls)
				}
			}
		})
	}
}

func TestLateCommittedStartCompletionDoesNotResumeMeasurement(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	held := &heldCompleteStart{Store: store, entered: make(chan struct{}), release: make(chan struct{}), commitBeforeWait: true}
	sim := &countedSimulator{}
	engine := newTestEngine(t, held, sim, uuid.New())
	events, _ := engine.CommandEvents()
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	created, err := controller.Create(context.Background(), command(10))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	startID := uuid.New()
	if _, err := controller.Start(context.Background(), id, contract.StartTestCommand{CommandId: startID, ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	state := engine.State()
	if state.PendingAction == nil {
		t.Fatal("missing action")
	}
	done := make(chan error, 1)
	go func() {
		_, err := controller.Confirm(context.Background(), id, state.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: state.Revision})
		done <- err
	}()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("completion did not commit")
	}
	state = waitFor(t, engine, func(state State) bool { return state.Stop != nil && state.Stop.Status == "completed" })
	if sim.starts.Load() != 1 || sim.stops.Load() != 1 || len(state.Measurements) != 0 {
		t.Fatalf("late completion state: %+v", state)
	}
	close(held.release)
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("completion: %v", err)
	}
	found := false
	for {
		select {
		case event := <-events:
			if event.Status.CommandId == startID && event.Status.Status == "completed" {
				found = true
			}
		default:
			if !found {
				t.Fatal("confirmed completion not reported")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
}

func TestFullDiagnosticQueueDoesNotBlockStop(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	sim := &countedSimulator{}
	engine := newTestEngine(t, store, sim, uuid.New())
	_, dropped := engine.CommandEvents()
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	created, err := controller.Create(context.Background(), command(1))
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	for range 64 {
		engine.emitCommand(contract.CommandStatus{CommandId: uuid.New(), Status: "completed"}, nil)
	}
	for range 2 {
		engine.emitCommand(contract.CommandStatus{CommandId: uuid.New(), Status: "completed"}, nil)
	}
	for range 3 {
		engine.emitCommand(contract.CommandStatus{CommandId: uuid.New(), Status: "failed"}, nil)
	}
	select {
	case <-dropped:
	default:
		t.Fatal("diagnostic drop not signalled")
	}
	info, failed := engine.TakeCommandDrops()
	if info != 2 || failed != 3 {
		t.Fatalf("drop counts: info=%d error=%d", info, failed)
	}
	stopID := engine.State().Record.StopCommandId
	if _, err := controller.Stop(context.Background(), *created.TestId, stopID); err != nil {
		t.Fatal(err)
	}
	state := waitFor(t, engine, func(state State) bool { return state.Stop != nil && state.Stop.Status == "completed" })
	if sim.stops.Load() != 1 {
		t.Fatalf("stop did not execute: %+v", state)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatal(err)
	}
}

func TestRenewedControlKeepsPendingRejection(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	held := &heldUnavailableRejection{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	engine := newTestEngine(t, held, &countedSimulator{}, uuid.New())
	controller, err := engine.GrantControl()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Renew(time.Second); err != nil {
		t.Fatal(err)
	}
	cmd := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	done := make(chan error, 1)
	go func() { _, err := controller.Start(context.Background(), uuid.New(), cmd); done <- err }()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("rejection did not start")
	}
	if _, err := controller.Renew(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("renewed operation ended: %v", err)
	case <-time.After(1200 * time.Millisecond):
	}
	close(held.release)
	if err := <-done; !errors.Is(err, storage.ErrTestNotFound) {
		t.Fatalf("rejection: %v", err)
	}
	status, err := store.Command(context.Background(), cmd.CommandId)
	if err != nil || status.Status != "rejected" {
		t.Fatalf("rejection not saved: %+v %v", status, err)
	}
	controller.Revoke("ui_lost")
}
