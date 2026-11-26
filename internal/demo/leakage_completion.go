package demo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

const leakageMethod = "singlePointLeakageV1"

func (e *Engine) completeManualMeasurement(ctx context.Context, testID uuid.UUID, window measurementWindow, completion storage.MeasurementCompletion, area float64, actions ...contract.PendingAction) (storage.OperationResult, error) {
	samples, err := e.store.MeasurementWindowSamples(ctx, testID)
	if err != nil {
		return storage.OperationResult{}, err
	}
	calculation := manualLeakageCalculation(testID, window, completion.Elapsed, area, samples)
	return e.store.Complete(ctx, testID, completion, &calculation, actions...)
}

func manualLeakageCalculation(testID uuid.UUID, window measurementWindow, elapsed time.Duration, area float64, samples []storage.TimedMeasurement) contract.LeakageCalculation {
	meter := demoDiaphragm
	conditions := contract.LeakageConditions{
		Source: "simulatorProfile", TemperatureC: 22, RelativeHumidity: .45, AtmosphericPressurePa: 101325,
		OrificeDiameterM: meter.orificeM, PipeDiameterM: meter.pipeM,
		OrificeExpansionPerC: meter.orificeExpansion, PipeExpansionPerC: meter.pipeExpansion,
		DischargeCoefficient: meter.discharge, ApproachCoefficient: meter.approach,
		Expansibility: meter.expansibility, RoughnessCorrection: meter.roughness,
	}
	calculation := contract.LeakageCalculation{
		PointId: uuid.NewSHA1(testID, []byte(leakageMethod)), Method: leakageMethod,
		WindowStartedAt: window.start.UTC(), WindowEndedAt: window.end.UTC(),
		DurationSeconds: int(elapsed / time.Second), Conditions: conditions,
		Channels: make([]contract.LeakageCalculationChannels, 0, 2),
	}
	if len(samples) != 0 {
		calculation.FirstSequence = samples[0].Measurement.Sequence
		calculation.LastSequence = samples[len(samples)-1].Measurement.Sequence
	}
	observations := make([]leakageObservation, 0, len(samples))
	var dd2, dd3 bool
	for _, sample := range samples {
		m := sample.Measurement
		if m.Quality != "valid" || m.Value == nil || sample.Elapsed <= 0 || sample.Elapsed > elapsed {
			continue
		}
		observation := leakageObservation{elapsed: sample.Elapsed}
		if m.Channel == "dd1" {
			observation.pressure = m.Value
		} else if m.SelectedDifferential != nil && *m.SelectedDifferential && *m.Value >= 0 {
			observation.differential = m.Value
			dd2 = dd2 || m.Channel == "dd2"
			dd3 = dd3 || m.Channel == "dd3"
		} else {
			continue
		}
		observations = append(observations, observation)
	}
	if dd2 {
		calculation.Channels = append(calculation.Channels, contract.LeakageCalculationChannelsDd2)
	}
	if dd3 {
		calculation.Channels = append(calculation.Channels, contract.LeakageCalculationChannelsDd3)
	}
	// The simulator profile applies to the entire window. Supply its conditions
	// once and keep repeated synthetic readings out of the measurement history.
	if len(observations) != 0 {
		observations[0].temperature = &conditions.TemperatureC
		observations[0].atmospheric = &conditions.AtmosphericPressurePa
		observations[0].humidity = &conditions.RelativeHumidity
	}
	values, reason := calculateLeakage(elapsed, area, meter, observations)
	if values == nil {
		unavailable := contract.LeakageCalculationUnavailableReason(reason)
		calculation.UnavailableReason = &unavailable
	} else {
		calculation.Values = &contract.LeakageValues{
			TestPressurePa: values.pressure, MassFlowKgPerSecond: values.mass,
			WorkingFlowM3PerSecond: values.working, NormalizedFlowM3PerHour: values.normalized,
			SpecificLeakageLitresPerSecondPerSquareMetre: values.specific, ContributingSeconds: values.seconds,
		}
	}
	return calculation
}
