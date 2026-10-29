// Package demo executes the first, explicitly simplified simulator-only run.
package demo

import (
	"errors"
	"math"
	"time"
)

// Equipment is a non-blocking in-process demo port. Calls are serialized by Engine.
// It is not a contract for a real stand or its safety devices.
type Equipment interface {
	Ready() bool
	Start(pmaxPa float64) error
	Observe(elapsed time.Duration) (pressure, dd2, dd3 float64, err error)
	Stop() error
	Finish() error
}

// Simulator is deterministic and has no connection to real devices.
type Simulator struct {
	running bool
	pmax    float64
}

func NewSimulator() *Simulator   { return &Simulator{} }
func (s *Simulator) Ready() bool { return !s.running }
func (s *Simulator) Start(pmaxPa float64) error {
	if s.running || pmaxPa <= 0 || math.IsNaN(pmaxPa) || math.IsInf(pmaxPa, 0) {
		return errors.New("demo simulator cannot start")
	}
	s.running = true
	s.pmax = pmaxPa
	return nil
}
func (s *Simulator) Observe(elapsed time.Duration) (float64, float64, float64, error) {
	if !s.running || elapsed < 0 {
		return 0, 0, 0, errors.New("demo simulator is not running")
	}
	// These bounded synthetic values demonstrate changing channels, not stand physics.
	factor := 1 - 0.01*math.Abs(math.Sin(elapsed.Seconds()))
	pressure := s.pmax * factor
	return pressure, s.pmax * (0.01 + 0.002*math.Sin(elapsed.Seconds())), s.pmax * (0.008 + 0.001*math.Cos(elapsed.Seconds())), nil
}
func (s *Simulator) Stop() error { s.running = false; return nil }
func (s *Simulator) Finish() error {
	if !s.running {
		return errors.New("demo simulator is not running")
	}
	s.running = false
	return nil
}
