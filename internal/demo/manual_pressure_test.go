package demo

import (
	"testing"
	"time"
)

func TestManualPressureRequiresPreparationBeforeSearching(t *testing.T) {
	start := time.Date(2026, time.November, 10, 12, 0, 0, 0, time.UTC)
	run := newManualPressureRun(manualPressureParameters{targetPa: 1000})
	target := 1000.0
	if got := run.observePressure(start, &target); got != manualPressureNoDecision || run.phase != awaitingPreparation {
		t.Fatalf("observation before preparation: phase %v, decision %v", run.phase, got)
	}
	if got := run.confirm(start, confirmPreparation); got != startPressureSearch || run.phase != searchingPressure {
		t.Fatalf("confirmed preparation: phase %v, decision %v", run.phase, got)
	}
	if got := run.confirm(start.Add(time.Minute), confirmPreparation); got != manualPressureNoDecision {
		t.Fatalf("duplicate preparation requested another action: %v", got)
	}
	if got := run.observePressure(start.Add(599*time.Second), nil); got != manualPressureNoDecision || run.phase != searchingPressure {
		t.Fatalf("missing DD1 reached the target: phase %v, decision %v", run.phase, got)
	}
	if got := run.observePressure(start.Add(599*time.Second), &target); got != manualPressureNoDecision || run.phase != stabilizingBeforeOpening {
		t.Fatalf("target before deadline: phase %v, decision %v", run.phase, got)
	}
}

func TestManualPressureInitialSearchExpiresAtOriginalDeadline(t *testing.T) {
	start := time.Date(2026, time.November, 10, 12, 0, 0, 0, time.UTC)
	run := newManualPressureRun(manualPressureParameters{targetPa: 1000})
	run.confirm(start, confirmPreparation)
	run.confirm(start.Add(time.Minute), confirmPreparation)
	belowTarget := 999.0
	if got := run.observePressure(start.Add(600*time.Second-time.Nanosecond), &belowTarget); got != manualPressureNoDecision || run.phase != searchingPressure {
		t.Fatalf("pressure below target before deadline: phase %v, decision %v", run.phase, got)
	}
	if got := run.advanceTime(start.Add(600 * time.Second)); got != stopInitialPressureSearchTimedOut || run.phase != manualPressureInterrupted {
		t.Fatalf("deadline: phase %v, decision %v", run.phase, got)
	}
	target := 1000.0
	if got := run.observePressure(start.Add(601*time.Second), &target); got != manualPressureNoDecision || run.phase != manualPressureInterrupted {
		t.Fatalf("late target changed interruption: phase %v, decision %v", run.phase, got)
	}
}

func manualPressureTestRun(retryLimit int) manualPressureRun {
	return newManualPressureRun(manualPressureParameters{
		targetPa: 1000, preOpenHold: 10 * time.Second, postCloseHold: 15 * time.Second,
		retryLimit: retryLimit, measurementDuration: time.Minute,
	})
}

func expectManualPressure(t *testing.T, run *manualPressureRun, phase manualPressurePhase, got, want manualPressureDecision) {
	t.Helper()
	if run.phase != phase || got != want {
		t.Fatalf("phase %v, decision %v; want phase %v, decision %v", run.phase, got, phase, want)
	}
}

func TestManualPressureRequiresBothHoldsAndSeparateValveConfirmations(t *testing.T) {
	start := time.Date(2026, time.November, 10, 12, 0, 0, 0, time.UTC)
	run := manualPressureTestRun(0)
	target, lowerPreOpen, upperPostClose := 1000.0, 980.0, 1050.0
	run.confirm(start, confirmPreparation)
	reachedAt := start.Add(time.Second)
	run.observePressure(reachedAt, &target)
	expectManualPressure(t, &run, stabilizingBeforeOpening, run.confirm(reachedAt, confirmValveOpened), manualPressureNoDecision)
	for seconds := 1; seconds < 10; seconds++ {
		run.observePressure(reachedAt.Add(time.Duration(seconds)*time.Second), &target)
	}
	openingAt := reachedAt.Add(10 * time.Second)
	expectManualPressure(t, &run, stabilizingBeforeOpening, run.advanceTime(openingAt), manualPressureNoDecision)
	expectManualPressure(t, &run, awaitingValveOpening, run.observePressure(openingAt, &lowerPreOpen), manualPressureNoDecision)
	expectManualPressure(t, &run, awaitingValveOpening, run.confirm(openingAt, confirmValveClosed), manualPressureNoDecision)
	expectManualPressure(t, &run, awaitingValveClosing, run.confirm(openingAt, confirmValveOpened), manualPressureNoDecision)
	expectManualPressure(t, &run, awaitingValveClosing, run.confirm(openingAt, confirmValveOpened), manualPressureNoDecision)
	closedAt := openingAt.Add(time.Second)
	expectManualPressure(t, &run, stabilizingAfterClosing, run.confirm(closedAt, confirmValveClosed), manualPressureNoDecision)
	firstPostClose := closedAt.Add(time.Second)
	for seconds := 0; seconds < 15; seconds++ {
		run.observePressure(firstPostClose.Add(time.Duration(seconds)*time.Second), &upperPostClose)
	}
	expectManualPressure(t, &run, stabilizingAfterClosing, run.observePressure(firstPostClose.Add(15*time.Second-time.Nanosecond), &upperPostClose), manualPressureNoDecision)
	expectManualPressure(t, &run, measuringManualPressure, run.observePressure(firstPostClose.Add(15*time.Second), &upperPostClose), manualPressureNoDecision)
}

func TestManualPressureUsesIndependentRetryLimitsAndRestartsHolds(t *testing.T) {
	start := time.Date(2026, time.November, 10, 12, 0, 0, 0, time.UTC)
	run := manualPressureTestRun(2)
	target, belowTarget, highBeforeOpen, lowAfterClose := 1000.0, 999.0, 1021.0, 949.0
	run.confirm(start, confirmPreparation)
	run.observePressure(start, &target)
	for _, seconds := range []time.Duration{9, 19} {
		outsideAt := start.Add(seconds * time.Second)
		expectManualPressure(t, &run, searchingPressure, run.observePressure(outsideAt, &highBeforeOpen), startPressureSearch)
		expectManualPressure(t, &run, searchingPressure, run.observePressure(outsideAt.Add(500*time.Millisecond), &belowTarget), manualPressureNoDecision)
		expectManualPressure(t, &run, stabilizingBeforeOpening, run.observePressure(outsideAt.Add(time.Second), &target), manualPressureNoDecision)
	}
	expectManualPressure(t, &run, stabilizingBeforeOpening, run.observePressure(start.Add(29*time.Second), &target), manualPressureNoDecision)
	run.observePressure(start.Add(30*time.Second), &target)
	run.confirm(start.Add(30*time.Second), confirmValveOpened)
	run.confirm(start.Add(31*time.Second), confirmValveClosed)
	for _, seconds := range []time.Duration{32, 34} {
		outsideAt := start.Add(seconds * time.Second)
		expectManualPressure(t, &run, searchingPressure, run.observePressure(outsideAt, &lowAfterClose), startPressureSearch)
		expectManualPressure(t, &run, stabilizingAfterClosing, run.observePressure(outsideAt.Add(time.Second), &target), manualPressureNoDecision)
	}
	expectManualPressure(t, &run, manualPressureInterrupted, run.observePressure(start.Add(36*time.Second), &lowAfterClose), stopStabilizationRetriesExhausted)
}

func TestManualPressureRepeatSearchExpiryLeavesOutcomeOpen(t *testing.T) {
	start := time.Date(2026, time.November, 10, 12, 0, 0, 0, time.UTC)
	run := manualPressureTestRun(2)
	target, outside := 1000.0, 979.0
	run.confirm(start, confirmPreparation)
	run.observePressure(start.Add(590*time.Second), &target)
	retryAt := start.Add(599 * time.Second)
	expectManualPressure(t, &run, searchingPressure, run.observePressure(retryAt, &outside), startPressureSearch)
	expectManualPressure(t, &run, searchingPressure, run.advanceTime(retryAt.Add(599*time.Second)), manualPressureNoDecision)
	expectManualPressure(t, &run, pressureRetryExpired, run.observePressure(retryAt.Add(600*time.Second), &target), retryPressureSearchTimedOut)
	expectManualPressure(t, &run, pressureRetryExpired, run.observePressure(retryAt.Add(601*time.Second), &target), manualPressureNoDecision)
}

func TestManualPressureMissingObservationCannotProveContinuousHold(t *testing.T) {
	start := time.Date(2026, time.November, 10, 12, 0, 0, 0, time.UTC)
	run := manualPressureTestRun(0)
	target := 1000.0
	run.confirm(start, confirmPreparation)
	run.observePressure(start, &target)
	expectManualPressure(t, &run, stabilizingBeforeOpening, run.observePressure(start.Add(9*time.Second), nil), manualPressureNoDecision)
	resumed := start.Add(10 * time.Second)
	expectManualPressure(t, &run, stabilizingBeforeOpening, run.observePressure(resumed, &target), manualPressureNoDecision)
	expectManualPressure(t, &run, awaitingValveOpening, run.observePressure(resumed.Add(10*time.Second), &target), manualPressureNoDecision)
}

func TestManualPressureMeasuresOnlyAfterPostCloseHold(t *testing.T) {
	start := time.Now()
	run := manualPressureTestRun(0)
	target := 1000.0
	run.confirm(start, confirmPreparation)
	run.observePressure(start.Add(time.Second), &target)
	openingAt := start.Add(11 * time.Second)
	run.observePressure(openingAt, &target)
	run.confirm(openingAt, confirmValveOpened)
	closedAt := start.Add(12 * time.Second)
	run.confirm(closedAt, confirmValveClosed)
	run.observePressure(closedAt, &target)
	windowStart := start.Add(27 * time.Second)
	if _, ok := run.window.nextSampleAfter(windowStart); ok || run.window.contains(closedAt) {
		t.Fatal("measurement window opened during stabilization")
	}
	run.observePressure(windowStart, &target)
	if run.window.contains(windowStart.Add(-time.Nanosecond)) || run.window.contains(windowStart) {
		t.Fatal("stabilization observations belong to the measurement window")
	}
	windowEnd := windowStart.Add(time.Minute)
	previous := windowStart
	for index := 1; index <= 600; index++ {
		next, ok := run.window.nextSampleAfter(previous)
		want := windowStart.Add(time.Duration(index) * 100 * time.Millisecond)
		if !ok || !next.Equal(want) || !run.window.contains(next) {
			t.Fatalf("sample %d: next=%v ok=%t want %v inside the window", index, next, ok, want)
		}
		previous = next
	}
	if _, ok := run.window.nextSampleAfter(windowEnd); ok || run.window.contains(windowEnd.Add(time.Nanosecond)) {
		t.Fatal("a sample after measurement would be accepted")
	}
	if next, ok := run.window.nextSampleAfter(windowStart.Add(250 * time.Millisecond)); !ok || !next.Equal(windowStart.Add(300*time.Millisecond)) {
		t.Fatalf("late observation should leave a gap in the schedule: next=%v ok=%t", next, ok)
	}
	expectManualPressure(t, &run, measuringManualPressure, run.advanceTime(windowEnd.Add(-time.Nanosecond)), manualPressureNoDecision)
	expectManualPressure(t, &run, manualPressureMeasurementComplete, run.advanceTime(windowEnd), manualPressureNoDecision)
	if !run.window.contains(windowEnd) {
		t.Fatal("completion invalidated the observation at the measurement deadline")
	}
}
