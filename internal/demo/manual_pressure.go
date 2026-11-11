package demo

import (
	"math"
	"time"
)

const (
	pressureSearchDuration  = 600 * time.Second
	demoMeasurementInterval = 100 * time.Millisecond
)

type manualPressurePhase uint8

const (
	awaitingPreparation manualPressurePhase = iota
	searchingPressure
	stabilizingBeforeOpening
	awaitingValveOpening
	awaitingValveClosing
	stabilizingAfterClosing
	pressureRetryExpired
	measuringManualPressure
	manualPressureMeasurementComplete
	manualPressureInterrupted
)

type manualPressureAction uint8

const (
	// confirmPreparation covers all ten cycles and the closed valve.
	confirmPreparation manualPressureAction = iota + 1
	confirmValveOpened
	confirmValveClosed
)

type manualPressureDecision uint8

const (
	manualPressureNoDecision manualPressureDecision = iota
	startPressureSearch
	stopInitialPressureSearchTimedOut
	stopStabilizationRetriesExhausted
	retryPressureSearchTimedOut
)

type manualPressureParameters struct {
	targetPa            float64
	preOpenHold         time.Duration
	postCloseHold       time.Duration
	retryLimit          int
	measurementDuration time.Duration
}

// manualPressureRun takes phase decisions from confirmed actions and monotonic
// times supplied by the Engine. The Engine owns execution of those decisions.
type manualPressureRun struct {
	parameters         manualPressureParameters
	phase              manualPressurePhase
	stabilizationPhase manualPressurePhase
	retriesUsed        int
	searchDeadline     time.Time
	stableSince        time.Time
	window             measurementWindow
}

type measurementWindow struct {
	start time.Time
	end   time.Time
}

func (w measurementWindow) contains(at time.Time) bool {
	// The observation at start belongs to stabilization. The final sample may
	// land on the measurement deadline itself.
	return at.After(w.start) && !at.After(w.end)
}

func (w measurementWindow) nextSampleAfter(now time.Time) (time.Time, bool) {
	if !now.Before(w.end) {
		return time.Time{}, false
	}
	elapsed := max(0, now.Sub(w.start))
	next := w.start.Add(elapsed.Truncate(demoMeasurementInterval) + demoMeasurementInterval)
	if !w.contains(next) {
		return time.Time{}, false
	}
	return next, true
}

func newManualPressureRun(parameters manualPressureParameters) manualPressureRun {
	return manualPressureRun{parameters: parameters, phase: awaitingPreparation}
}

func (r *manualPressureRun) confirm(now time.Time, action manualPressureAction) manualPressureDecision {
	switch {
	case r.phase == awaitingPreparation && action == confirmPreparation:
		r.stabilizationPhase = stabilizingBeforeOpening
		return r.beginPressureSearch(now)
	case r.phase == awaitingValveOpening && action == confirmValveOpened:
		r.phase = awaitingValveClosing
	case r.phase == awaitingValveClosing && action == confirmValveClosed:
		r.stabilizationPhase = stabilizingAfterClosing
		r.phase = r.stabilizationPhase
		r.stableSince = time.Time{}
		r.retriesUsed = 0
	}
	return manualPressureNoDecision
}

func (r *manualPressureRun) beginPressureSearch(now time.Time) manualPressureDecision {
	r.phase = searchingPressure
	r.searchDeadline = now.Add(pressureSearchDuration)
	r.stableSince = time.Time{}
	return startPressureSearch
}

func (r *manualPressureRun) advanceTime(now time.Time) manualPressureDecision {
	switch {
	case r.phase == searchingPressure && !now.Before(r.searchDeadline):
		if r.retriesUsed > 0 {
			// The outcome of a repeat-search timeout awaits laboratory agreement.
			r.phase = pressureRetryExpired
			return retryPressureSearchTimedOut
		}
		r.phase = manualPressureInterrupted
		return stopInitialPressureSearchTimedOut
	case r.phase == measuringManualPressure && !now.Before(r.window.end):
		r.phase = manualPressureMeasurementComplete
	}
	return manualPressureNoDecision
}

// The Engine supplies scheduled DD1 observations in time order, passing nil
// for a missed or unusable observation. A clock tick alone cannot prove a hold.
func (r *manualPressureRun) observePressure(now time.Time, dd1 *float64) manualPressureDecision {
	valid := dd1 != nil && !math.IsNaN(*dd1) && !math.IsInf(*dd1, 0)
	switch r.phase {
	case searchingPressure:
		if decision := r.advanceTime(now); decision != manualPressureNoDecision {
			return decision
		}
		if valid && *dd1 >= r.parameters.targetPa {
			r.phase = r.stabilizationPhase
			if r.withinStabilizationBand(*dd1) {
				r.stableSince = now
			}
		}
	case stabilizingBeforeOpening, stabilizingAfterClosing:
		if !valid {
			r.stableSince = time.Time{}
			return manualPressureNoDecision
		}
		return r.observeStabilization(now, *dd1)
	}
	return manualPressureNoDecision
}

func (r *manualPressureRun) withinStabilizationBand(pressurePa float64) bool {
	tolerance := 0.02
	if r.stabilizationPhase == stabilizingAfterClosing {
		tolerance = 0.05
	}
	target := r.parameters.targetPa
	return pressurePa >= target*(1-tolerance) && pressurePa <= target*(1+tolerance)
}

func (r *manualPressureRun) observeStabilization(now time.Time, pressurePa float64) manualPressureDecision {
	if !r.withinStabilizationBand(pressurePa) {
		if r.retriesUsed >= r.parameters.retryLimit {
			r.phase = manualPressureInterrupted
			return stopStabilizationRetriesExhausted
		}
		r.retriesUsed++
		return r.beginPressureSearch(now)
	}
	if r.stableSince.IsZero() {
		r.stableSince = now
	}
	stableFor := now.Sub(r.stableSince)
	switch {
	case r.phase == stabilizingBeforeOpening && stableFor >= r.parameters.preOpenHold:
		r.phase = awaitingValveOpening
	case r.phase == stabilizingAfterClosing && stableFor >= r.parameters.postCloseHold:
		r.phase = measuringManualPressure
		r.window = measurementWindow{start: now, end: now.Add(r.parameters.measurementDuration)}
	}
	return manualPressureNoDecision
}
