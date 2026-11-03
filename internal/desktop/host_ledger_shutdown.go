package desktop

import (
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

// Shutdown ledger operations run under the same session mutex. They inspect or
// update evidence only; the session coordinator owns network ordering and waits.
func stopOutcomeUnconfirmed(active *contract.TestRecord, id uuid.UUID, status contract.CommandStatus) bool {
	if active == nil || active.Stage != "finished" || id != active.StopCommandId || status.Status == contract.CommandStatusStatusCompleted {
		return false
	}
	return status.Status != contract.CommandStatusStatusRejected || (active.Outcome != "completed" && active.Outcome != "interrupted")
}

func (s *hostLedger) shutdownEvidence(snapshot *contract.Snapshot, backendID uuid.UUID) (*contract.TestRecord, bool) {
	var active *contract.TestRecord
	if snapshot != nil && snapshot.ActiveTest != nil {
		copy := *snapshot.ActiveTest
		active = &copy
	}
	if s.activeTestID != uuid.Nil && s.activeStopID != uuid.Nil && (active == nil || active.TestId != s.activeTestID) {
		active = &contract.TestRecord{TestId: s.activeTestID, StopCommandId: s.activeStopID, Stage: "running"}
	}
	active = s.savedRecordLocked(active)
	unconfirmed := s.displaced != nil
	if active != nil && (active.Stage != "finished" || active.SaveStatus != "saved") {
		unconfirmed = true
	}
	if s.activeTestID != uuid.Nil && (active == nil || active.TestId != s.activeTestID) {
		unconfirmed = true
	}
	for id, status := range s.commands {
		if !terminalCommand(status.Status) || !s.createCovered(id, status, snapshot, backendID) || stopOutcomeUnconfirmed(active, id, status) {
			unconfirmed = true
			break
		}
	}
	return active, unconfirmed
}
func (s *hostLedger) shutdownPending(active *contract.TestRecord) ([]uuid.UUID, bool) {
	unconfirmed := false
	pending := make([]uuid.UUID, 0, len(s.commands))
	for id, status := range s.commands {
		if !terminalCommand(status.Status) {
			pending = append(pending, id)
		} else if stopOutcomeUnconfirmed(active, id, status) {
			unconfirmed = true
		}
	}
	return pending, unconfirmed
}
func (s *hostLedger) applyShutdownObservation(attempt trackedCommand, status contract.CommandStatus, err error) (contract.CommandStatus, error) {
	if existing, ok := s.commands[attempt.id]; ok && s.commandPayloads[attempt.id] == attempt.identity && !s.inFlight[attempt.id] && !s.ambiguous[attempt.id] {
		if err == nil {
			s.commands[attempt.id] = mergeCommandStatus(existing, status)
		}
		if terminalCommand(s.commands[attempt.id].Status) {
			status, err = s.commands[attempt.id], nil
		}
	} else {
		err = errSessionUnavailable
	}
	return status, err
}
func (s *hostLedger) allCreatesCovered(snapshot *contract.Snapshot, backendID uuid.UUID) bool {
	covered := true
	for id, status := range s.commands {
		if !s.createCovered(id, status, snapshot, backendID) {
			covered = false
		}
	}
	return covered
}
func (s *hostLedger) applyStartAfterSave(id uuid.UUID, status contract.CommandStatus, err error) (contract.CommandStatus, error) {
	existing, tracked := s.commands[id]
	if tracked && !s.inFlight[id] && !s.ambiguous[id] && err == nil {
		s.commands[id] = mergeCommandStatus(existing, status)
		status = s.commands[id]
	} else {
		err = ErrSessionShutdownUnconfirmed
	}
	return status, err
}
func (s *hostLedger) registerShutdownStop(active *contract.TestRecord, identity string) bool {
	stopID := active.StopCommandId
	_, exists := s.commands[stopID]
	tracked := exists && s.commandPayloads[stopID] == identity
	if !tracked {
		if exists && (!terminalCommand(s.commands[stopID].Status) || s.inFlight[stopID] || s.ambiguous[stopID]) {
			s.displaced = &displacedAttempt{id: stopID, identity: s.commandPayloads[stopID], inFlight: s.inFlight[stopID]}
		}
		s.commands[stopID] = contract.CommandStatus{CommandId: stopID, TestId: &active.TestId, Status: contract.CommandStatusStatusUnknown}
		s.commandPayloads[stopID] = identity
		s.inFlight[stopID] = true
		delete(s.ambiguous, stopID)
	}
	return tracked
}
func (s *hostLedger) finishShutdownStop(stopID uuid.UUID, identity string, status contract.CommandStatus, err error) {
	if s.commandPayloads[stopID] == identity {
		delete(s.inFlight, stopID)
		if err == nil {
			s.commands[stopID] = status
		} else {
			var failure *transport.RequestError
			if errors.As(err, &failure) && !failure.OutcomeUnknown && failure.Code != "command_conflict" {
				current := s.commands[stopID]
				current.Status = contract.CommandStatusStatusRejected
				if failure.Code != "" {
					reason := failure.Code
					current.Reason = &reason
				}
				s.commands[stopID] = current
			}
		}
	}
}

func (s *hostLedger) shutdownAttempt(id uuid.UUID) (trackedCommand, bool) {
	return trackedCommand{id: id, identity: s.commandPayloads[id]}, s.inFlight[id] || s.ambiguous[id]
}
func (s *hostLedger) commandEvidence(id uuid.UUID) contract.CommandStatus { return s.commands[id] }
func (s *hostLedger) exactCommandEvidence(id uuid.UUID, identity string) (contract.CommandStatus, bool) {
	status, exists := s.commands[id]
	return status, exists && s.commandPayloads[id] == identity
}
func (s *hostLedger) hasDisplaced() bool { return s.displaced != nil }
func (c trackedCommand) isStop() bool    { return strings.HasPrefix(c.identity, "stop:") }
func (c trackedCommand) startsTest(testID uuid.UUID) bool {
	return strings.HasPrefix(c.identity, "start:"+testID.String()+":")
}
