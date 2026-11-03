package desktop

import (
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

// hostLedger holds host evidence about attempts, not authoritative core state.
// All operations run under demoSession.mu; it owns no socket, clock or worker.
type hostLedger struct {
	commands         map[uuid.UUID]contract.CommandStatus
	commandPayloads  map[uuid.UUID]string
	inFlight         map[uuid.UUID]bool
	ambiguous        map[uuid.UUID]bool
	knownStops       map[uuid.UUID]uuid.UUID
	activeTestID     uuid.UUID
	activeStopID     uuid.UUID
	activeGeneration uint64
	savedTests       map[uuid.UUID]uuid.UUID
	displaced        *displacedAttempt
}
type displacedAttempt struct {
	id       uuid.UUID
	identity string
	inFlight bool
}

func terminalCommand(status contract.CommandStatusStatus) bool {
	return status == contract.CommandStatusStatusCompleted || status == contract.CommandStatusStatusRejected || status == contract.CommandStatusStatusFailed
}

func mergeCommandStatus(current, next contract.CommandStatus) contract.CommandStatus {
	if terminalCommand(current.Status) || (current.Status == contract.CommandStatusStatusAccepted && next.Status == contract.CommandStatusStatusUnknown) {
		return current
	}
	if next.TestId == nil && current.TestId != nil {
		id := *current.TestId
		next.TestId = &id
	}
	return next
}

// Older snapshots can arrive after the response; only later matching snapshots
// or confirmed saved records cover a completed Create.
func (s *hostLedger) createCovered(id uuid.UUID, status contract.CommandStatus, snapshot *contract.Snapshot, backendID uuid.UUID) bool {
	identity := s.commandPayloads[id]
	if !strings.HasPrefix(identity, "create:") || status.Status != contract.CommandStatusStatusCompleted {
		return true
	}
	if status.TestId == nil || *status.TestId == uuid.Nil {
		return false
	}
	inputValidation.once.Do(func() { inputValidation.validator, inputValidation.err = contract.NewInputValidator() })
	if inputValidation.err != nil {
		return false
	}
	var command contract.CreateTestCommand
	input, err := inputValidation.validator.InspectCanonical("CreateTestCommand", []byte(strings.TrimPrefix(identity, "create:")), &command)
	if err != nil || !input.Valid || command.CommandId != id {
		return false
	}
	if s.savedTests[*status.TestId] != uuid.Nil {
		return true
	}
	return snapshot != nil && snapshot.BackendRunId == backendID && snapshot.Revision > command.ExpectedRevision
}

func (s *hostLedger) rememberStopLocked(record contract.TestRecord) {
	if record.StopCommandId == uuid.Nil || record.TestId == uuid.Nil {
		return
	}
	if record.Stage != "finished" {
		if s.activeTestID != record.TestId || s.activeStopID != record.StopCommandId {
			s.activeGeneration++
		}
		s.activeTestID, s.activeStopID = record.TestId, record.StopCommandId
	} else if s.activeTestID == record.TestId {
		s.activeTestID, s.activeStopID = uuid.Nil, uuid.Nil
		s.activeGeneration++
	}
	s.rememberStopPairLocked(record)
}

func (s *hostLedger) rememberSaved(record contract.TestRecord, snapshot *contract.Snapshot) {
	if record.TestId == uuid.Nil || record.StopCommandId == uuid.Nil || record.Stage != "finished" || record.SaveStatus != "saved" {
		return
	}
	if s.activeTestID == record.TestId && s.activeStopID != record.StopCommandId {
		return
	}
	matched := s.activeTestID == record.TestId && s.activeStopID == record.StopCommandId
	if snapshot != nil && snapshot.ActiveTest != nil {
		current := snapshot.ActiveTest
		if current.TestId == record.TestId {
			if current.StopCommandId != record.StopCommandId {
				return
			}
			matched = true
		}
	}
	for id, status := range s.commands {
		if strings.HasPrefix(s.commandPayloads[id], "create:") && status.Status == contract.CommandStatusStatusCompleted && status.TestId != nil && *status.TestId == record.TestId {
			matched = true
		}
	}
	if matched {
		// Keep evidence only while a bounded command or the current snapshot refers to it.
		for testID := range s.savedTests {
			referenced := s.activeTestID == testID || (snapshot != nil && snapshot.ActiveTest != nil && snapshot.ActiveTest.TestId == testID)
			for id, status := range s.commands {
				if strings.HasPrefix(s.commandPayloads[id], "create:") && status.TestId != nil && *status.TestId == testID {
					referenced = true
					break
				}
			}
			if !referenced {
				delete(s.savedTests, testID)
			}
		}
		s.savedTests[record.TestId] = record.StopCommandId
	}
}

func (s *hostLedger) savedRecordLocked(record *contract.TestRecord) *contract.TestRecord {
	if record == nil || record.TestId == uuid.Nil || record.StopCommandId == uuid.Nil || s.savedTests[record.TestId] != record.StopCommandId {
		return record
	}
	copy := *record
	copy.Stage, copy.SaveStatus = "finished", "saved"
	return &copy
}

func (s *hostLedger) rememberStopPairLocked(record contract.TestRecord) {
	if record.StopCommandId == uuid.Nil || record.TestId == uuid.Nil {
		return
	}
	if _, exists := s.knownStops[record.StopCommandId]; !exists && len(s.knownStops) >= 64 {
		for id := range s.knownStops {
			delete(s.knownStops, id)
			break
		}
	}
	s.knownStops[record.StopCommandId] = record.TestId
}

func (s *hostLedger) reservedStop(id uuid.UUID, snapshot *contract.Snapshot) (uuid.UUID, bool) {
	if id != uuid.Nil && id == s.activeStopID && s.activeTestID != uuid.Nil {
		return s.activeTestID, true
	}
	if snapshot != nil && snapshot.ActiveTest != nil {
		record := snapshot.ActiveTest
		if id != uuid.Nil && id == record.StopCommandId && record.TestId != uuid.Nil {
			return record.TestId, true
		}
	}
	testID, exists := s.knownStops[id]
	return testID, exists
}

func (s *hostLedger) stopStatus(id uuid.UUID, snapshot *contract.Snapshot) (contract.CommandStatus, bool) {
	current, tracked := s.commands[id]
	identity := s.commandPayloads[id]
	if testID, reserved := s.reservedStop(id, snapshot); reserved {
		if tracked && strings.HasPrefix(identity, "stop:"+testID.String()+":") {
			return current, true
		}
		return contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusUnknown}, true
	}
	if tracked && strings.HasPrefix(identity, "stop:") {
		return current, true
	}
	return contract.CommandStatus{}, false
}

func (s *hostLedger) ordinaryCommandBlockedLocked() bool {
	if s.displaced != nil {
		return true
	}
	for id, command := range s.commands {
		if strings.HasPrefix(s.commandPayloads[id], "stop:") && !terminalCommand(command.Status) && command.TestId != nil && *command.TestId == s.activeTestID && s.activeTestID != uuid.Nil {
			return true
		}
	}
	return false
}

func newHostLedger() hostLedger {
	return hostLedger{commands: make(map[uuid.UUID]contract.CommandStatus), commandPayloads: make(map[uuid.UUID]string), inFlight: make(map[uuid.UUID]bool), ambiguous: make(map[uuid.UUID]bool), knownStops: make(map[uuid.UUID]uuid.UUID), savedTests: make(map[uuid.UUID]uuid.UUID)}
}

type trackedCommand struct {
	id       uuid.UUID
	identity string
}

func (s *hostLedger) pendingOrdinary(snapshot *contract.Snapshot) []trackedCommand {
	pending := make([]trackedCommand, 0, len(s.commands))
	for id, command := range s.commands {
		identity := s.commandPayloads[id]
		_, reservedStop := s.reservedStop(id, snapshot)
		if !terminalCommand(command.Status) && !s.inFlight[id] && !s.ambiguous[id] {
			if !reservedStop && !strings.HasPrefix(identity, "stop:") {
				pending = append(pending, trackedCommand{id: id, identity: identity})
			}
		}
	}
	return pending
}
func (s *hostLedger) applyOrdinaryObservation(item trackedCommand, command contract.CommandStatus, snapshot *contract.Snapshot) {
	_, reservedStop := s.reservedStop(item.id, snapshot)
	identityMatches := !reservedStop
	if existing, ok := s.commands[item.id]; ok && identityMatches && s.commandPayloads[item.id] == item.identity && !s.inFlight[item.id] && !s.ambiguous[item.id] {
		s.commands[item.id] = mergeCommandStatus(existing, command)
	}
}
func (s *hostLedger) observeSnapshot(snapshot contract.Snapshot) {
	if snapshot.ActiveTest != nil {
		s.rememberStopLocked(*snapshot.ActiveTest)
	} else if s.activeTestID != uuid.Nil {
		s.activeTestID, s.activeStopID = uuid.Nil, uuid.Nil
		s.activeGeneration++
	}
}

func (s *hostLedger) prepareAttempt(id uuid.UUID, identity string, stop, stopAllowed bool, snapshot *contract.Snapshot) (contract.CommandStatus, bool, error) {
	if !stop {
		if _, reserved := s.reservedStop(id, snapshot); reserved {
			return contract.CommandStatus{}, true, ErrCommandConflict
		}
	}
	if existing, ok := s.commands[id]; ok {
		if s.commandPayloads[id] != identity {
			if !stop {
				return contract.CommandStatus{}, true, ErrCommandConflict
			}
			if !stopAllowed {
				return contract.CommandStatus{}, true, errSessionUnavailable
			}
			if !terminalCommand(existing.Status) || s.inFlight[id] || s.ambiguous[id] {
				s.displaced = &displacedAttempt{id: id, identity: s.commandPayloads[id], inFlight: s.inFlight[id]}
			}
			delete(s.commands, id)
			delete(s.commandPayloads, id)
			delete(s.inFlight, id)
			delete(s.ambiguous, id)
		} else {
			if stop && (existing.Status == contract.CommandStatusStatusUnknown || existing.Status == contract.CommandStatusStatusRejected) && !s.inFlight[id] {
				delete(s.commands, id)
				delete(s.commandPayloads, id)
				delete(s.ambiguous, id)
			} else {
				return existing, true, nil
			}
		}
	}
	return contract.CommandStatus{}, false, nil
}
func (s *hostLedger) registerAttempt(id uuid.UUID, identity string, stop, valid bool, snapshot *contract.Snapshot, backendID uuid.UUID) error {
	limit := 64
	if stop {
		limit++
	}
	if len(s.commands) >= limit {
		for previous, status := range s.commands {
			activeStop := previous == s.activeStopID || (snapshot != nil && snapshot.ActiveTest != nil && previous == snapshot.ActiveTest.StopCommandId)
			if terminalCommand(status.Status) && !s.inFlight[previous] && (!activeStop || !strings.HasPrefix(s.commandPayloads[previous], "stop:")) && s.createCovered(previous, status, snapshot, backendID) {
				delete(s.commands, previous)
				delete(s.commandPayloads, previous)
				delete(s.ambiguous, previous)
				break
			}
		}
	}
	if !valid || len(s.commands) >= limit {
		return errors.Join(errSessionUnavailable, errCommandNotSent)
	}
	status := contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatusUnknown}
	if stop {
		if s.activeStopID == id && s.activeTestID != uuid.Nil {
			testID := s.activeTestID
			status.TestId = &testID
		} else if testID, ok := s.knownStops[id]; ok {
			status.TestId = &testID
		}
	}
	s.commands[id] = status
	s.commandPayloads[id] = identity
	s.inFlight[id] = true
	return nil
}
func (s *hostLedger) finishDisplaced(id uuid.UUID, identity string, resultErr error) {
	if s.displaced != nil && s.displaced.id == id && s.displaced.identity == identity {
		var failure *transport.RequestError
		if errors.Is(resultErr, errCommandNotSent) || (errors.As(resultErr, &failure) && !failure.OutcomeUnknown) {
			s.displaced = nil
		} else {
			s.displaced.inFlight = false
		}
	}
}
func (s *hostLedger) finishAttempt(id uuid.UUID, identity string, stop, previouslyPresent bool, result contract.CommandStatus, err error) (contract.CommandStatus, error) {
	if s.commandPayloads[id] != identity {
		return contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatusUnknown}, err
	}
	delete(s.inFlight, id)
	current := s.commands[id]
	if err == nil && !terminalCommand(current.Status) {
		current = mergeCommandStatus(current, result)
		s.commands[id] = current
	}
	if err != nil {
		var failure *transport.RequestError
		if errors.As(err, &failure) && failure.OutcomeUnknown && previouslyPresent {
			s.ambiguous[id] = true
		}
		if errors.As(err, &failure) && !failure.OutcomeUnknown {
			if !stop {
				delete(s.commands, id)
				delete(s.commandPayloads, id)
				delete(s.ambiguous, id)
			} else if failure.Code != "command_conflict" {
				current.Status = contract.CommandStatusStatusRejected
				if failure.Code != "" {
					reason := failure.Code
					current.Reason = &reason
				}
				s.commands[id] = current
			}
		}
	}
	return current, err
}

func (s *hostLedger) matchesAttempt(id uuid.UUID, identity string) bool {
	return s.commandPayloads[id] == identity
}
func (s *hostLedger) discardPrepared(id uuid.UUID, identity string) {
	if s.matchesAttempt(id, identity) {
		delete(s.commands, id)
		delete(s.commandPayloads, id)
		delete(s.inFlight, id)
	}
}

func (s *hostLedger) applyHistory(record contract.TestRecord, requestedID uuid.UUID, generation uint64, snapshot *contract.Snapshot) {
	if record.TestId == requestedID {
		s.rememberSaved(record, snapshot)
	}
	finishedInSnapshot := snapshot != nil && snapshot.ActiveTest != nil && snapshot.ActiveTest.TestId == record.TestId && snapshot.ActiveTest.Stage == "finished"
	differentActive := snapshot != nil && snapshot.ActiveTest != nil && snapshot.ActiveTest.TestId != record.TestId && snapshot.ActiveTest.Stage != "finished"
	if !finishedInSnapshot && !differentActive && (s.activeGeneration == generation || s.activeTestID == record.TestId) {
		s.rememberStopLocked(record)
	} else {
		s.rememberStopPairLocked(record)
	}
}
func (s *hostLedger) historyGeneration() uint64 { return s.activeGeneration }

func (s *hostLedger) stopAdmission(id, commandID uuid.UUID, identity string, snapshot *contract.Snapshot) (contract.CommandStatus, bool, error) {
	var active *contract.TestRecord
	if snapshot != nil {
		active = snapshot.ActiveTest
	}
	valid := (active != nil && active.Stage != "finished" && active.TestId == id && active.StopCommandId == commandID) || (s.activeTestID == id && s.activeStopID == commandID)
	previous, tracked := s.commands[commandID]
	knownRepeat := tracked && s.commandPayloads[commandID] == identity
	reservedTest, reserved := s.reservedStop(commandID, snapshot)
	if !valid {
		if knownRepeat {
			return previous, false, nil
		}
		if reserved && reservedTest != id {
			return contract.CommandStatus{}, false, ErrCommandConflict
		}
		return contract.CommandStatus{}, false, errSessionUnavailable
	}
	return contract.CommandStatus{}, true, nil
}
func (s *hostLedger) applyStopStatus(id uuid.UUID, identity string, status, result contract.CommandStatus, snapshot *contract.Snapshot) contract.CommandStatus {
	if current, tracked := s.commands[id]; tracked && s.commandPayloads[id] == identity && !s.inFlight[id] && !s.ambiguous[id] {
		status = mergeCommandStatus(current, result)
		s.commands[id] = status
	} else if current, stillStop := s.stopStatus(id, snapshot); stillStop {
		status = current
	}
	return status
}
func (s *hostLedger) applyGenericStatus(id uuid.UUID, identity string, status contract.CommandStatus, snapshot *contract.Snapshot) contract.CommandStatus {
	if status, isStop := s.stopStatus(id, snapshot); isStop {
		return status
	}
	if current, tracked := s.commands[id]; tracked {
		if s.commandPayloads[id] != identity || s.inFlight[id] || s.ambiguous[id] {
			status = current
		} else {
			status = mergeCommandStatus(current, status)
			s.commands[id] = status
		}
	}
	return status
}

type commandStatusRead struct {
	status                 contract.CommandStatus
	identity               string
	isStop, lookup, cached bool
}

func (s *hostLedger) planCommandStatus(id uuid.UUID, snapshot *contract.Snapshot) commandStatusRead {
	plan := commandStatusRead{identity: s.commandPayloads[id]}
	if status, isStop := s.stopStatus(id, snapshot); isStop {
		plan.status = status
		plan.isStop = true
		plan.lookup = status.TestId != nil && strings.HasPrefix(plan.identity, "stop:") && !terminalCommand(status.Status) && !s.inFlight[id] && !s.ambiguous[id]
		return plan
	}
	if current, tracked := s.commands[id]; tracked && (s.inFlight[id] || s.ambiguous[id]) {
		plan.status = current
		plan.cached = true
	}
	return plan
}

func (s *hostLedger) applyDisplacedExact(commandID uuid.UUID, identity string, status contract.CommandStatus, conflict bool, err error) bool {
	busy := false
	if displaced := s.displaced; displaced != nil && displaced.id == commandID && displaced.identity == identity {
		if displaced.inFlight {
			busy = true
		} else if conflict || (err == nil && status.Status != contract.CommandStatusStatusUnknown) {
			s.displaced = nil
		}
	}
	return busy
}
func (s *hostLedger) applyTrackedExact(commandID uuid.UUID, identity, kind string, status contract.CommandStatus, conflict bool, err error) bool {
	busy := false
	if current, ok := s.commands[commandID]; ok && s.commandPayloads[commandID] == identity {
		if s.inFlight[commandID] {
			busy = true
		} else if conflict && kind != "stop" {
			delete(s.commands, commandID)
			delete(s.commandPayloads, commandID)
			delete(s.ambiguous, commandID)
		} else if err == nil && status.Status != contract.CommandStatusStatusUnknown {
			s.commands[commandID] = mergeCommandStatus(current, status)
			delete(s.ambiguous, commandID)
		}
	}
	return busy
}
