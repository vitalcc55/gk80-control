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
	stopPressureAboveMaximum
	stopDifferentialAboveMaximum
	stopPressureBelowLimit
	stopPressureAboveLimit
	stopPressureChannelLost
	stopDifferentialChannelsLost
)

type manualPressureParameters struct {
	targetPa             float64
	preOpenHold          time.Duration
	postCloseHold        time.Duration
	retryLimit           int
	measurementDuration  time.Duration
	lowerPa, upperPa     float64
	lowerHold, upperHold time.Duration
}

// manualPressureRun takes phase decisions from confirmed actions and monotonic
// times supplied by the Engine. The Engine owns execution of those decisions.
type manualPressureRun struct {
	parameters             manualPressureParameters
	phase                  manualPressurePhase
	stabilizationPhase     manualPressurePhase
	retriesUsed            int
	searchDeadline         time.Time
	stableSince            time.Time
	window                 measurementWindow
	lowerSince, upperSince time.Time
	missed                 [3]int
	activeDifferential     int
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
			return r.retryPressureSearch(now)
		}
		r.phase = manualPressureInterrupted
		return stopInitialPressureSearchTimedOut
	case r.phase == measuringManualPressure && !now.Before(r.window.end):
		r.phase = manualPressureMeasurementComplete
	}
	return manualPressureNoDecision
}

// The Engine supplies DD1 observations in acquisition order, passing nil for
// an unusable observation. A clock tick alone cannot prove a hold.
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
		return r.retryPressureSearch(now)
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

func (r *manualPressureRun) retryPressureSearch(now time.Time) manualPressureDecision {
	if r.retriesUsed >= r.parameters.retryLimit {
		r.phase = manualPressureInterrupted
		return stopStabilizationRetriesExhausted
	}
	r.retriesUsed++
	return r.beginPressureSearch(now)
}

func (r *manualPressureRun) observe(now time.Time, readings [3]*float64) manualPressureDecision {
	for i, value := range readings {
		if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
			readings[i] = nil
			r.missed[i] = min(10, r.missed[i]+1)
		} else {
			r.missed[i] = 0
		}
	}
	dd1 := readings[0]
	switch {
	case dd1 != nil && *dd1 > r.parameters.targetPa*1.1:
		return r.interrupt(stopPressureAboveMaximum)
	case readings[1] != nil && *readings[1] > 10000 || readings[2] != nil && *readings[2] > 10000:
		return r.interrupt(stopDifferentialAboveMaximum)
	case r.missed[0] == 10:
		return r.interrupt(stopPressureChannelLost)
	case r.missed[1] == 10 && r.missed[2] == 10:
		return r.interrupt(stopDifferentialChannelsLost)
	}
	if r.activeDifferential == 0 && (readings[1] != nil || readings[2] != nil) {
		r.activeDifferential = 1
	}
	if r.activeDifferential == 1 && readings[2] != nil && (readings[1] == nil || *readings[1] >= 2400) {
		r.activeDifferential = 2
	} else if r.activeDifferential == 2 && readings[1] != nil && (readings[2] == nil || *readings[2] <= 2300) {
		r.activeDifferential = 1
	}
	if r.phase == measuringManualPressure {
		if outsideFor(now, &r.lowerSince, dd1 != nil && *dd1 < r.parameters.lowerPa, r.parameters.lowerHold) {
			return r.interrupt(stopPressureBelowLimit)
		}
		if outsideFor(now, &r.upperSince, dd1 != nil && *dd1 > r.parameters.upperPa, r.parameters.upperHold) {
			return r.interrupt(stopPressureAboveLimit)
		}
	}
	return r.observePressure(now, dd1)
}

func (r *manualPressureRun) interrupt(reason manualPressureDecision) manualPressureDecision {
	r.phase = manualPressureInterrupted
	return reason
}

func (r *manualPressureRun) breakPressureHolds() {
	r.stableSince = time.Time{}
	r.lowerSince = time.Time{}
	r.upperSince = time.Time{}
}

func outsideFor(now time.Time, since *time.Time, outside bool, hold time.Duration) bool {
	if !outside {
		*since = time.Time{}
		return false
	}
	if since.IsZero() {
		*since = now
	}
	return now.Sub(*since) >= hold
}
