package demo

import (
	"math"
	"time"
)

const normalAirDensity = 1.204 // kg/m³ at 20 °C and 101325 Pa

type diaphragm struct {
	orificeM, pipeM, orificeExpansion, pipeExpansion float64
	discharge, approach, expansibility, roughness    float64
}

var demoDiaphragm = diaphragm{.03, .10, 17e-6, 17e-6, .61, 1.00407477, .998, 1}

type leakageObservation struct {
	elapsed                                                    time.Duration
	pressure, differential, temperature, atmospheric, humidity *float64
}

type leakageValues struct {
	pressure, mass, working, normalized, specific float64
	seconds                                       int
}

type leakageMean struct {
	total float64
	count int
}

func (m *leakageMean) add(value float64) {
	m.total += value
	m.count++
}

func (m leakageMean) value() float64 { return m.total / float64(m.count) }

type leakageSecond struct {
	pressure, temperature, atmospheric, humidity leakageMean
	differentials                                []float64
}

func interpolateLeakageConditions(seconds []leakageSecond, field func(*leakageSecond) *leakageMean) bool {
	previous := -1
	for i := range seconds {
		next := field(&seconds[i])
		if next.count == 0 {
			continue
		}
		start := next.value()
		if previous >= 0 {
			start = field(&seconds[previous]).value()
		}
		for j := previous + 1; j < i; j++ {
			fraction := float64(j-previous) / float64(i-previous)
			field(&seconds[j]).add(start + fraction*(next.value()-start))
		}
		previous = i
	}
	if previous < 0 {
		return false
	}
	for i := previous + 1; i < len(seconds); i++ {
		field(&seconds[i]).add(field(&seconds[previous]).value())
	}
	return true
}

func calculateLeakage(duration time.Duration, area float64, meter diaphragm, observations []leakageObservation) (*leakageValues, string) {
	seconds := make([]leakageSecond, (duration+time.Second-1)/time.Second)
	var pressure leakageMean
	for _, observation := range observations {
		if observation.elapsed <= 0 || observation.elapsed > duration {
			continue
		}
		second := &seconds[(observation.elapsed-1)/time.Second]
		if observation.pressure != nil && finiteLeakageValue(*observation.pressure) {
			second.pressure.add(*observation.pressure)
			pressure.add(*observation.pressure)
		}
		if observation.temperature != nil && finiteLeakageValue(*observation.temperature) {
			second.temperature.add(*observation.temperature)
		}
		if observation.atmospheric != nil && finiteLeakageValue(*observation.atmospheric) {
			second.atmospheric.add(*observation.atmospheric)
		}
		if observation.humidity != nil && finiteLeakageValue(*observation.humidity) {
			second.humidity.add(*observation.humidity)
		}
		if observation.differential != nil && finiteLeakageValue(*observation.differential) && *observation.differential >= 0 {
			second.differentials = append(second.differentials, *observation.differential)
		}
	}
	if !interpolateLeakageConditions(seconds, func(s *leakageSecond) *leakageMean { return &s.pressure }) {
		return nil, "missingTestPressure"
	}
	fields := []func(*leakageSecond) *leakageMean{
		func(s *leakageSecond) *leakageMean { return &s.temperature },
		func(s *leakageSecond) *leakageMean { return &s.atmospheric },
		func(s *leakageSecond) *leakageMean { return &s.humidity },
	}
	for _, field := range fields {
		if !interpolateLeakageConditions(seconds, field) {
			return nil, "missingAirConditions"
		}
	}
	var mass, working leakageMean
	for _, second := range seconds {
		if len(second.differentials) == 0 {
			continue
		}
		t := second.temperature.value()
		density := leakageAirDensity(t, second.atmospheric.value()+second.pressure.value(), second.humidity.value())
		orifice := meter.orificeM * (1 + meter.orificeExpansion*(t-20))
		pipe := meter.pipeM * (1 + meter.pipeExpansion*(t-20))
		if density <= 0 || !finiteLeakageValue(density) || orifice <= 0 || orifice >= pipe {
			return nil, "invalidCalculationInputs"
		}
		factor := math.Pi * orifice * orifice / 4 * meter.discharge * meter.approach * meter.expansibility * meter.roughness
		var secondMass leakageMean
		for _, differential := range second.differentials {
			secondMass.add(factor * math.Sqrt(2*density*differential))
		}
		mass.add(secondMass.value())
		working.add(secondMass.value() / density)
	}
	if mass.count == 0 {
		return nil, "noUsableDifferential"
	}
	normalized := mass.value() * 3600 / normalAirDensity
	specific := normalized / (3.6 * area)
	if area <= 0 || !finiteLeakageValue(pressure.value()) || !finiteLeakageValue(specific) || !finiteLeakageValue(working.value()) {
		return nil, "invalidCalculationInputs"
	}
	return &leakageValues{pressure.value(), mass.value(), working.value(), normalized, specific, mass.count}, ""
}

func finiteLeakageValue(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func leakageAirDensity(temperature, absolutePressure, humidity float64) float64 {
	kelvin := temperature + 273.15
	if kelvin <= 0 || humidity < 0 || humidity > 1 {
		return math.NaN()
	}
	vapour := humidity * 610.94 * math.Exp(17.625*temperature/(temperature+243.04))
	if absolutePressure <= vapour {
		return math.NaN()
	}
	return (absolutePressure-vapour)/(287.05*kelvin) + vapour/(461.52*kelvin)
}
