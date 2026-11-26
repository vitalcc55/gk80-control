package demo

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

func reading(value float64) *float64 { return &value }

func TestLeakageUsesEqualWeightForFilledSeconds(t *testing.T) {
	input := []leakageObservation{
		{elapsed: 0, pressure: reading(9000), differential: reading(10000)},
		{elapsed: 100 * time.Millisecond, pressure: reading(1000), differential: reading(20), temperature: reading(22), atmospheric: reading(101325), humidity: reading(.45)},
		{elapsed: time.Second, pressure: reading(1000), differential: reading(80)},
		{elapsed: 2 * time.Second, pressure: reading(900), differential: reading(45), temperature: reading(24), atmospheric: reading(101325), humidity: reading(.45)},
		{elapsed: 3 * time.Second, differential: reading(-5)},
		{elapsed: 3*time.Second + time.Nanosecond, differential: reading(10000)},
	}
	result, reason := calculateLeakage(3*time.Second, .5, demoDiaphragm, input)
	if reason != "" || result == nil {
		t.Fatalf("calculation unavailable: %s", reason)
	}
	for name, pair := range map[string][2]float64{
		"pressure":   {result.pressure, 966.6666666666666},
		"mass":       {result.mass, .004486006098587828},
		"working":    {result.working, .0037461982635121844},
		"normalized": {result.normalized, 13.413307271525069},
		"specific":   {result.specific, 7.451837373069482},
	} {
		if math.Abs(pair[0]-pair[1]) > 1e-10 {
			t.Errorf("%s = %.15g, want %.15g", name, pair[0], pair[1])
		}
	}
}

func TestLeakageUsesSelectedChannelAndMonotonicOffsets(t *testing.T) {
	start := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	window := measurementWindow{start: start, end: start.Add(2 * time.Second)}
	selected, standby := true, false
	samples := []storage.TimedMeasurement{
		{Elapsed: time.Second, Measurement: contract.Measurement{Sequence: 1, Channel: "dd1", Quality: "valid", Value: reading(1000)}},
		{Elapsed: time.Second, Measurement: contract.Measurement{Sequence: 2, Channel: "dd2", Quality: "valid", Value: reading(20), SelectedDifferential: &selected}},
		{Elapsed: time.Second, Measurement: contract.Measurement{Sequence: 3, Channel: "dd3", Quality: "valid", Value: reading(9000), SelectedDifferential: &standby}},
		{Elapsed: 2 * time.Second, Measurement: contract.Measurement{Sequence: 4, Channel: "dd1", Quality: "valid", Value: reading(1000)}},
		{Elapsed: 2 * time.Second, Measurement: contract.Measurement{Sequence: 5, Channel: "dd2", Quality: "valid", Value: reading(9000), SelectedDifferential: &standby}},
		{Elapsed: 2 * time.Second, Measurement: contract.Measurement{Sequence: 6, Channel: "dd3", Quality: "valid", Value: reading(80), SelectedDifferential: &selected}},
	}
	for index := range samples {
		// Wall clock goes backwards while the next acquisition remains in the next second.
		samples[index].Measurement.ObservedAt = start.Add(-time.Duration(index) * time.Hour)
	}
	calculation := manualLeakageCalculation(uuid.New(), window, 2*time.Second, .5, samples)
	if got := calculation.Channels; len(got) != 2 || got[0] != "dd2" || got[1] != "dd3" {
		t.Fatalf("channels = %v", got)
	}
	values := calculation.Values
	if values == nil || values.ContributingSeconds != 2 || values.TestPressurePa != 1000 ||
		math.Abs(values.MassFlowKgPerSecond-.004495165487948163) > 1e-10 {
		t.Fatalf("calculation across switch and wall-clock step: %+v", values)
	}
}

func TestLeakageMissingInputsAndZero(t *testing.T) {
	input := []leakageObservation{
		{elapsed: time.Second, pressure: reading(900), differential: reading(0), temperature: reading(22), atmospheric: reading(101325), humidity: reading(.45)},
		{elapsed: 2 * time.Second, differential: reading(0)},
	}
	got, reason := calculateLeakage(2*time.Second, .5, demoDiaphragm, input)
	if got == nil || reason != "" || got.pressure != 900 || got.normalized != 0 || got.specific != 0 || got.seconds != 2 {
		t.Fatalf("valid zero and endpoint interpolation: %+v %q", got, reason)
	}
	input[0].temperature = nil
	if got, reason := calculateLeakage(2*time.Second, .5, demoDiaphragm, input); got != nil || reason != "missingAirConditions" {
		t.Fatalf("missing temperature: %+v %q", got, reason)
	}
}
