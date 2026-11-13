package demo

import (
	"context"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

func manualParameters(p contract.TestParameters) manualPressureParameters {
	return manualPressureParameters{
		targetPa: p.PmaxPa, preOpenHold: time.Duration(p.PreOpenStabilizationSeconds) * time.Second,
		postCloseHold: time.Duration(p.PostCloseStabilizationSeconds) * time.Second,
		retryLimit:    p.PostCloseStabilizationRetries, measurementDuration: time.Duration(p.MeasurementSeconds) * time.Second,
		lowerPa: *p.LowerPressureLimitPa, upperPa: *p.UpperPressureLimitPa,
		lowerHold: time.Duration(*p.LowerPressureHoldSeconds) * time.Second, upperHold: time.Duration(*p.UpperPressureHoldSeconds) * time.Second,
	}
}

func newRunAction(testID uuid.UUID, kind contract.PendingActionKind) contract.PendingAction {
	instruction := ""
	switch kind {
	case "confirmPreparation":
		instruction = "Подтвердите, что выполнили 10 циклов открытия и закрытия и оставили клапан закрытым."
	case "confirmValveOpen":
		instruction = "Откройте клапан и подтвердите открытие."
	case "confirmValveClosed":
		instruction = "Закройте клапан и подтвердите закрытие."
	case "confirmStopped":
		instruction = "Подтвердите, что вал остановился."
	}
	return contract.PendingAction{ActionId: uuid.New(), TestId: testID, Kind: &kind, Instruction: instruction}
}

func (r *manualPressureRun) differential() string {
	switch r.activeDifferential {
	case 1:
		return "dd2"
	case 2:
		return "dd3"
	}
	return ""
}

func (r *manualPressureRun) presentation(now time.Time) (*contract.RunPhase, *contract.MeasurementTiming) {
	var phase contract.RunPhase
	var start time.Time
	var duration time.Duration
	switch r.phase {
	case awaitingPreparation:
		phase = "awaitingPreparation"
	case searchingPressure:
		phase, start, duration = "searchingPressure", r.searchDeadline.Add(-pressureSearchDuration), pressureSearchDuration
	case stabilizingBeforeOpening:
		phase, start, duration = "stabilizingBeforeOpening", r.stableSince, r.parameters.preOpenHold
	case awaitingValveOpening:
		phase = "awaitingValveOpening"
	case awaitingValveClosing:
		phase = "awaitingValveClosing"
	case stabilizingAfterClosing:
		phase, start, duration = "stabilizingAfterClosing", r.stableSince, r.parameters.postCloseHold
	case measuringManualPressure:
		phase, start, duration = "measuring", r.window.start, r.parameters.measurementDuration
	default:
		return nil, nil
	}
	if duration == 0 {
		return &phase, nil
	}
	elapsed := time.Duration(0)
	if !start.IsZero() {
		elapsed = min(duration, max(0, now.Sub(start)))
	}
	return &phase, &contract.MeasurementTiming{ElapsedSeconds: elapsed.Seconds(), RemainingSeconds: (duration - elapsed).Seconds()}
}

func (decision manualPressureDecision) stopReason() string {
	switch decision {
	case stopInitialPressureSearchTimedOut:
		return "pressure_search_timeout"
	case stopStabilizationRetriesExhausted:
		return "stabilization_retries_exhausted"
	case stopPressureAboveMaximum:
		return "pressure_above_maximum"
	case stopDifferentialAboveMaximum:
		return "differential_above_maximum"
	case stopPressureBelowLimit:
		return "pressure_below_limit"
	case stopPressureAboveLimit:
		return "pressure_above_limit"
	case stopPressureChannelLost:
		return "pressure_channel_lost"
	case stopDifferentialChannelsLost:
		return "differential_channels_lost"
	}
	return ""
}

// The ticker owns observation and queue closure. Stop cancels the run directly;
// database calls never hold the lock shared with Stop.
func (e *Engine) runManualPressure(current *run, testID uuid.UUID) {
	ticker := time.NewTicker(demoMeasurementInterval)
	defer ticker.Stop()
	var gridOrigin time.Time
	var previousTick time.Time
	var queue chan samplePacket
	defer func() {
		if queue != nil {
			close(queue)
		}
	}()
	var sequence int64
	var readingSequence int64
	for {
		var tick time.Time
		select {
		case <-current.ctx.Done():
			return
		case tick = <-ticker.C:
		}
		e.mu.Lock()
		if e.active != current || current.stopping {
			e.mu.Unlock()
			return
		}
		// Normalize the ticker's scheduled timestamp to its own grid. The
		// observation itself is timestamped separately when it is taken.
		if gridOrigin.IsZero() {
			gridOrigin = tick
		}
		scheduled := gridOrigin.Add(tick.Sub(gridOrigin).Round(demoMeasurementInterval))
		now := e.runTime(scheduled)
		if !previousTick.IsZero() && scheduled.Sub(previousTick) > demoMeasurementInterval {
			current.manual.breakPressureHolds()
		}
		previousTick = scheduled
		current.observedAt = now
		phase := current.manual.phase
		if phase == measuringManualPressure && now.After(current.manual.window.end) {
			e.finishManualMeasurementLocked(current, queue)
			queue = nil
			return
		}
		pressure, dd2, dd3, err := e.equipment.Observe(now.Sub(current.searchStarted))
		var readings [3]*float64
		if err == nil {
			for i, value := range [3]float64{pressure, dd2, dd3} {
				if !math.IsNaN(value) && !math.IsInf(value, 0) {
					readings[i] = &value
				}
			}
		}
		previousDifferential := current.manual.differential()
		decision := current.manual.observe(now, readings)
		channel := current.manual.differential()
		waitingForValve := phase == awaitingValveOpening || phase == awaitingValveClosing
		if decision == startPressureSearch {
			current.searchStarted = now
		}
		if current.manual.phase == measuringManualPressure && phase != measuringManualPressure {
			current.currentReadings = nil
			queue = make(chan samplePacket, sampleQueueLimit)
			current.writerDone = make(chan struct{})
			go e.writeSamples(current, testID, queue)
		} else if phase != measuringManualPressure && (!waitingForValve || previousDifferential != channel) {
			current.currentReadings = manualReadings(readings, &readingSequence)
		}
		if phase == measuringManualPressure && current.manual.window.contains(now) {
			elapsed := now.Sub(current.manual.window.start)
			latest := manualReadings(readings, &sequence)
			packet := make(samplePacket, 0, len(latest))
			for _, measurement := range latest {
				packet = append(packet, storage.TimedMeasurement{Measurement: measurement, Elapsed: elapsed})
			}
			select {
			case queue <- packet:
				current.measurementBatch++
				current.measurementTiming = &contract.MeasurementTiming{ElapsedSeconds: elapsed.Seconds(), RemainingSeconds: current.manual.window.end.Sub(now).Seconds()}
				current.pendingSamples += len(packet)
				current.record.SaveStatus = "pending"
				current.latest = latest
			default:
				e.acceptStopLocked(current, "system", "measurement_queue_full:3")
				current.stopDetails.MeasurementsLost = true
				e.mu.Unlock()
				go e.finishStop(current)
				return
			}
		}
		if reason := decision.stopReason(); reason != "" {
			e.acceptStopLocked(current, "system", reason)
			e.mu.Unlock()
			go e.finishStop(current)
			return
		}
		if phase == measuringManualPressure && !now.Before(current.manual.window.end) {
			e.finishManualMeasurementLocked(current, queue)
			queue = nil
			return
		}
		requestOpening := phase != awaitingValveOpening && current.manual.phase == awaitingValveOpening
		// Keep the readings at the confirmation boundary. The protection
		// still uses every poll, and a channel change updates the projection.
		if phase != current.manual.phase || !waitingForValve || previousDifferential != channel {
			e.signal()
		}
		e.mu.Unlock()
		if requestOpening && !e.requestValveOpening(current, testID) {
			return
		}
		if previousDifferential != "" && previousDifferential != channel {
			ctx, cancel := context.WithTimeout(current.ctx, measurementWriteTimeout)
			err := e.store.RecordDifferentialSwitch(ctx, testID, channel)
			cancel()
			if err != nil {
				e.requestStop(current, "storage_error")
				return
			}
		}
	}
}

func (e *Engine) runTime(tick time.Time) time.Time {
	if e.measurementNow != nil {
		return e.measurementNow()
	}
	return tick
}

func manualReadings(readings [3]*float64, sequence *int64) []contract.Measurement {
	now := time.Now().UTC()
	measuredAt := now.Format(time.RFC3339Nano)
	latest := make([]contract.Measurement, 0, 3)
	for index, channel := range [...]contract.MeasurementChannel{"dd1", "dd2", "dd3"} {
		*sequence++
		quantity := contract.MeasurementQuantity("differentialPressure")
		if index == 0 {
			quantity = "pressure"
		}
		sample := contract.Measurement{Sequence: *sequence, Channel: channel, Quantity: quantity, Unit: "Pa", ObservedAt: now, Quality: "unavailable", Source: "simulator"}
		if readings[index] != nil {
			sample.Value, sample.MeasuredAt, sample.Quality = readings[index], &measuredAt, "valid"
		}
		latest = append(latest, sample)
	}
	return latest
}

func (e *Engine) requestValveOpening(current *run, testID uuid.UUID) bool {
	ctx, cancel := context.WithTimeout(current.ctx, measurementWriteTimeout)
	defer cancel()
	result, err := e.store.RequestRunAction(ctx, newRunAction(testID, "confirmValveOpen"))
	if err != nil || !result.Applied || result.Saved == nil {
		e.requestStop(current, "storage_error")
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active != current || current.stopping {
		return false
	}
	current.record, current.revision, current.pending = result.Saved.Record, result.Saved.Revision, result.Saved.PendingAction
	e.revision = current.revision
	e.signal()
	return true
}

// Called with the Engine lock. Publishes the outcome and stops the simulator
// before waiting for the measurement writer or final commit.
func (e *Engine) finishManualMeasurementLocked(current *run, queue chan samplePacket) {
	close(queue)
	if err := e.equipment.Finish(); err != nil {
		e.acceptStopLocked(current, "system", "simulator_error")
		e.mu.Unlock()
		go e.finishStop(current)
		return
	}
	testID := current.record.TestId
	elapsed := current.manual.parameters.measurementDuration
	action := newRunAction(testID, "confirmStopped")
	current.naturalDone = true
	current.finishDone = make(chan struct{})
	defer close(current.finishDone)
	current.finishElapsed = elapsed
	current.stopConfirmation = &action
	current.record.Stage, current.record.Outcome = "finished", "completed"
	current.record.DataQuality, current.record.SaveStatus = "pending", "pending"
	current.revision++
	e.revision = current.revision
	e.signal()
	e.mu.Unlock()
	<-current.writerDone
	e.mu.Lock()
	if current.writerErr != nil {
		current.record.SaveStatus, current.record.DataQuality = "failed", "unknown"
		e.signal()
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), measurementWriteTimeout)
	defer cancel()
	result, err := e.store.Complete(ctx, testID, elapsed, action)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active != current {
		return
	}
	if err != nil || result.Saved == nil {
		current.record.SaveStatus, current.record.DataQuality = saveFailure(err, false), "unknown"
	} else {
		current.record, current.revision, current.pending = result.Saved.Record, result.Saved.Revision, result.Saved.PendingAction
		current.finalized = true
		e.revision = current.revision
	}
	e.signal()
}
