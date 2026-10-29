package demo

import (
	"context"
	"errors"
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

type countedSimulator struct{ starts, stops atomic.Int32 }

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
func (s *countedSimulator) Stop() error { s.stops.Add(1); return nil }
func (*countedSimulator) Finish() error { return nil }

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
