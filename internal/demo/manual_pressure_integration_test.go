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
}

func (s *recordingSimulator) Observe(elapsed time.Duration) (float64, float64, float64, error) {
	s.observations.Add(1)
	return s.Simulator.Observe(elapsed)
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
			t.Logf("saved %d measurements from %d observation attempts across the pass, quality %s", count, simulator.observations.Load(), record.DataQuality)
		})
	}
}
