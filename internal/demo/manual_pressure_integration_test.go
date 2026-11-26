package demo

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

type recordingSimulator struct {
	*Simulator
	observations atomic.Int64
	elapsed      []time.Duration
}

func (s *recordingSimulator) Observe(elapsed time.Duration) (float64, float64, float64, error) {
	s.observations.Add(1)
	s.elapsed = append(s.elapsed, elapsed)
	return s.Simulator.Observe(elapsed)
}

type recordedPolls struct {
	Repository
	samples []storage.TimedMeasurement
}

func (s *recordedPolls) RecordMeasurements(_ context.Context, _ uuid.UUID, samples []storage.TimedMeasurement) error {
	s.samples = append(s.samples, samples...)
	return nil
}

func (s *recordedPolls) MeasurementWindowSamples(context.Context, uuid.UUID) ([]storage.TimedMeasurement, error) {
	return s.samples, nil
}

func (s *recordedPolls) Complete(context.Context, uuid.UUID, time.Duration, *contract.LeakageCalculation, ...contract.PendingAction) (storage.OperationResult, error) {
	return storage.OperationResult{Saved: &storage.SavedState{}}, nil
}

func newManualPollingTest(t *testing.T, gridStart time.Time) (*Engine, *recordedPolls, *recordingSimulator) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	parameters := command(60).Parameters
	current := &run{
		ctx:           ctx,
		record:        contract.TestRecord{TestId: uuid.New(), Stage: "running", Parameters: parameters},
		searchStarted: gridStart.Add(-3 * time.Second),
		manual: manualPressureRun{
			parameters: manualParameters(parameters), phase: stabilizingAfterClosing,
			stabilizationPhase: stabilizingAfterClosing, stableSince: gridStart.Add(-10 * time.Second),
		},
	}
	repo := &recordedPolls{}
	simulator := &recordingSimulator{Simulator: NewSimulator()}
	if err := simulator.Start(1000); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{active: current, store: repo, equipment: simulator, changed: make(chan struct{}, 1)}
	return engine, repo, simulator
}

func TestManualPressureRejectsBufferedPollAfterWindow(t *testing.T) {
	gridStart := time.Now().Add(-3 * time.Second)
	engine, repo, simulator := newManualPollingTest(t, gridStart)
	current := engine.active
	ticks := make(chan time.Time, 2)
	ticks <- gridStart
	done := make(chan struct{})
	go func() {
		defer close(done)
		engine.pollManualPressure(current, current.record.TestId, ticks)
	}()
	waitFor(t, engine, func(s State) bool { return s.RunPhase != nil && *s.RunPhase == "measuring" })

	// A buffered tick precedes the deadline, but its observation would be taken
	// after it. The subsequent tick lets the uncorrected loop also finish.
	end := gridStart.Add(2 * time.Second)
	engine.mu.Lock()
	current.manual.window = measurementWindow{start: end.Add(-60 * time.Second), end: end}
	engine.mu.Unlock()
	ticks <- end.Add(-demoMeasurementInterval)
	ticks <- time.Now().Add(demoMeasurementInterval)
	select {
	case <-done:
	case <-current.ctx.Done():
		t.Fatal("measurement did not finish")
	}
	if got := simulator.observations.Load(); got != 1 || len(repo.samples) != 0 {
		t.Fatalf("observations: %d, archived samples: %d; only the stabilization observation is due", got, len(repo.samples))
	}
}

func TestManualPressureMarksPollAfterSkippedSlot(t *testing.T) {
	gridStart := time.Now().Add(10 * time.Second)
	engine, repo, _ := newManualPollingTest(t, gridStart)
	current := engine.active
	now := gridStart
	engine.measurementNow = func() time.Time { return now }
	ticks := make(chan time.Time, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		engine.pollManualPressure(current, current.record.TestId, ticks)
	}()
	ticks <- gridStart
	waitFor(t, engine, func(s State) bool { return s.RunPhase != nil && *s.RunPhase == "measuring" })
	for i, offset := range []time.Duration{100 * time.Millisecond, 300 * time.Millisecond} {
		engine.mu.Lock()
		now = gridStart.Add(offset)
		engine.mu.Unlock()
		ticks <- gridStart.Add(offset)
		waitFor(t, engine, func(s State) bool { return s.MeasurementBatch != nil && *s.MeasurementBatch == int64(i+1) })
	}
	engine.mu.Lock()
	now = gridStart.Add(61 * time.Second)
	engine.mu.Unlock()
	ticks <- gridStart.Add(61 * time.Second)
	select {
	case <-done:
	case <-current.ctx.Done():
		t.Fatal("measurement did not finish")
	}
	if len(repo.samples) != 6 {
		t.Fatalf("archived samples: %d, want two packets", len(repo.samples))
	}
	for i, sample := range repo.samples {
		if sample.Measurement.GapBefore != (i == 3) {
			t.Fatalf("unexpected gap marker for sample %+v at index %d", sample, i)
		}
	}
}

func TestManualPressureSQLitePass(t *testing.T) {
	for _, tc := range []struct {
		seconds   int
		interrupt bool
		realTime  bool
	}{{60, false, false}, {60, true, false}, {60, false, true}, {300, false, true}} {
		t.Run(fmt.Sprintf("%ds_stop_%t_realtime_%t", tc.seconds, tc.interrupt, tc.realTime), func(t *testing.T) {
			if tc.realTime && os.Getenv("GK80_CHECK_REALTIME_MEASUREMENT") != "1" {
				t.Skip("enable the real-time SQLite load check explicitly")
			}
			if tc.realTime {
				t.Parallel()
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(tc.seconds+45)*time.Second)
			defer cancel()
			dir := t.TempDir()
			store, err := storage.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			simulator := &recordingSimulator{Simulator: NewSimulator()}
			engine, err := NewEngine(ctx, store, simulator, uuid.New())
			if err != nil {
				t.Fatal(err)
			}
			if !tc.realTime {
				origin := time.Now()
				engine.measurementNow = func() time.Time { return origin.Add(time.Since(origin) * 40) }
			}
			wait := func(match func(State) bool) State {
				t.Helper()
				for {
					state := engine.State()
					if match(state) {
						return state
					}
					select {
					case <-engine.Changes():
					case <-ctx.Done():
						t.Fatalf("state not reached: %+v", state)
					}
				}
			}
			created, err := engine.Create(ctx, command(float64(tc.seconds)))
			if err != nil || created.TestId == nil {
				t.Fatalf("create: %v %v", created, err)
			}
			id := *created.TestId
			initial := engine.State()
			if _, err := engine.Start(ctx, id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: initial.Revision, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
				t.Fatal(err)
			}
			actions := map[uuid.UUID]bool{}
			for _, kind := range []contract.PendingActionKind{"confirmPreparation", "confirmValveOpen", "confirmValveClosed"} {
				state := wait(func(s State) bool {
					return s.PendingAction != nil && s.PendingAction.Kind != nil && *s.PendingAction.Kind == kind
				})
				if actions[state.PendingAction.ActionId] || state.MeasurementBatch != nil {
					t.Fatalf("bad action state: %+v", state)
				}
				if kind == "confirmValveOpen" && (state.CurrentReadings == nil ||
					len(*state.CurrentReadings) != 3 || (*state.CurrentReadings)[0].Value == nil ||
					*(*state.CurrentReadings)[0].Value != 1000 || len(state.Measurements) != 0) {
					t.Fatalf("pressure was not projected independently of the archive: %+v", state)
				}
				actions[state.PendingAction.ActionId] = true
				request := contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: state.Revision}
				if _, err := engine.Confirm(ctx, id, state.PendingAction.ActionId, request); err != nil {
					t.Fatal(err)
				}
				if _, err := engine.Confirm(ctx, id, state.PendingAction.ActionId, request); err != nil {
					t.Fatalf("exact replay: %v", err)
				}
			}
			if tc.interrupt {
				state := wait(func(s State) bool { return s.MeasurementBatch != nil })
				if _, err := engine.Stop(ctx, id, state.Record.StopCommandId); err != nil {
					t.Fatal(err)
				}
			}
			finished := wait(func(s State) bool { return s.Record.Stage == "finished" && s.Record.SaveStatus == "saved" })
			wantOutcome := contract.TestRecordOutcome("completed")
			if tc.interrupt {
				wantOutcome = "interrupted"
			}
			if finished.Record.Outcome != wantOutcome || finished.PendingAction == nil || *finished.PendingAction.Kind != "confirmStopped" {
				t.Fatalf("finish: %+v", finished)
			}
			next := command(60)
			next.ExpectedRevision = finished.Revision
			if _, err := engine.Create(ctx, next); err != ErrBusy {
				t.Fatalf("new test before confirmation: %v", err)
			}
			if _, err := engine.Confirm(ctx, id, finished.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: finished.Revision}); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := storage.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reopened.Close() }()
			record, err := reopened.Test(ctx, id)
			if err != nil || record.Outcome != wantOutcome {
				t.Fatalf("reopen: %+v %v", record, err)
			}
			if tc.interrupt {
				if record.LeakageCalculation != nil {
					t.Fatal("interrupted measurement has a final point")
				}
			} else if calculation := record.LeakageCalculation; calculation == nil || calculation.Values == nil ||
				calculation.Values.SpecificLeakageLitresPerSecondPerSquareMetre <= 0 ||
				!reflect.DeepEqual(calculation, finished.Record.LeakageCalculation) {
				t.Fatalf("missing or changed calculation: %+v", calculation)
			}
			var count int64
			for {
				page, err := reopened.MeasurementsPage(ctx, id, count, 500)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range page.Items {
					count++
					if item.Sequence != count {
						t.Fatalf("sample sequence: %d, want %d", item.Sequence, count)
					}
				}
				if len(page.Items) < 500 {
					break
				}
			}
			if finished.MeasurementBatch == nil || count != *finished.MeasurementBatch*3 {
				t.Fatalf("saved %d samples, snapshot batch %v", count, finished.MeasurementBatch)
			}
			if tc.realTime && count < int64(tc.seconds*30)*99/100 {
				t.Fatalf("saved %d of about %d samples", count, tc.seconds*30)
			}
			if tc.realTime {
				samples, err := reopened.MeasurementWindowSamples(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				engine.mu.Lock()
				window, searchStarted := engine.active.manual.window, engine.active.searchStarted
				attempts := append([]time.Duration(nil), simulator.elapsed...)
				engine.mu.Unlock()
				// The simulator's elapsed arguments and the window use the same
				// monotonic clock. UTC can move independently during the run.
				polls := len(samples) / 3
				if len(samples) != int(count) || polls > len(attempts) {
					t.Fatalf("inconsistent archived samples: %d, paged samples: %d, polls: %d", len(samples), count, len(attempts))
				}
				attempts = attempts[len(attempts)-polls:]
				for i, sample := range samples {
					acquiredAt := searchStarted.Add(attempts[i/3])
					if !window.contains(acquiredAt) || sample.Elapsed != acquiredAt.Sub(window.start) {
						t.Fatalf("sample %d outside the monotonic window or saved with a different offset", sample.Measurement.Sequence)
					}
				}
			}
			t.Logf("saved %d measurements from %d observation attempts across the pass, quality %s", count, simulator.observations.Load(), record.DataQuality)
		})
	}
}
