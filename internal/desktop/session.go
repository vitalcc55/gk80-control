package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

const (
	uiHeartbeatFreshness = 10 * time.Second
	leaseDuration        = 30 * time.Second
	streamProbeInterval  = 2 * time.Second
	streamProbeFreshness = 6 * time.Second
	sessionRequestLimit  = 3 * time.Second
	sessionSyncLimit     = 5 * time.Second
	sessionRecoveryLimit = 15 * time.Second
)

var errSessionUnavailable = errors.New("demo session is not confirmed")
var errSessionBusy = errors.New("demo session operation is in progress")
var errCommandNotSent = errors.New("this command attempt was not sent")

// ErrSessionShutdownUnconfirmed reports an unknown session or test outcome, not process exit.
var ErrSessionShutdownUnconfirmed = errors.New("session shutdown could not be confirmed")

type sessionStream interface {
	Next(context.Context) (contract.Event, error)
	Poll(context.Context) (contract.Event, bool, error)
	Pending() bool
	PendingSince() time.Time
	Snapshot(context.Context) (contract.Snapshot, error)
	Probe(context.Context) error
	Done() <-chan struct{}
	Close(context.Context) error
}

type sessionClient interface {
	AcquireLease(context.Context, uuid.UUID, *uuid.UUID) (contract.UiLease, error)
	RenewLease(context.Context, uuid.UUID) (contract.UiLease, error)
	ReleaseLease(context.Context, uuid.UUID) error
	Snapshot(context.Context, uuid.UUID) (contract.Snapshot, error)
	Command(context.Context, uuid.UUID, uuid.UUID) (contract.CommandStatus, error)
	ListTests(context.Context, uuid.UUID, contract.ListTestsParams) (contract.TestList, error)
	Test(context.Context, uuid.UUID, uuid.UUID) (contract.TestRecord, error)
	Readiness(context.Context, uuid.UUID, uuid.UUID) (contract.Readiness, error)
	Measurements(context.Context, uuid.UUID, uuid.UUID, int64, int) (contract.MeasurementPage, error)
	Audit(context.Context, uuid.UUID, uuid.UUID, int64, int) (contract.AuditPage, error)
	CreateTest(context.Context, uuid.UUID, contract.CreateTestCommand) (contract.CommandStatus, error)
	CreateTestRaw(context.Context, uuid.UUID, uuid.UUID, []byte) (contract.CommandStatus, error)
	StartTest(context.Context, uuid.UUID, uuid.UUID, contract.StartTestCommand) (contract.CommandStatus, error)
	StartTestRaw(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, []byte) (contract.CommandStatus, error)
	ConfirmAction(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, contract.ConfirmActionCommand) (contract.CommandStatus, error)
	ConfirmActionRaw(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, []byte) (contract.CommandStatus, error)
	LookupCreateExact(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, []byte) (contract.CommandStatus, error)
	LookupStartExact(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, []byte) (contract.CommandStatus, error)
	LookupConfirmExact(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, []byte) (contract.CommandStatus, error)
	LookupStopExact(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, []byte) (contract.CommandStatus, error)
	StopTest(context.Context, uuid.UUID, uuid.UUID, contract.StopTestCommand) (contract.CommandStatus, error)
	StopTestRaw(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, []byte) (contract.CommandStatus, error)
	openStream(context.Context, uuid.UUID, uuid.UUID) (sessionStream, contract.Snapshot, error)
}

type nativeSessionClient struct{ *transport.Client }

func (c nativeSessionClient) openStream(ctx context.Context, lease, runID uuid.UUID) (sessionStream, contract.Snapshot, error) {
	stream, snapshot, err := c.OpenStream(ctx, lease, runID)
	if stream == nil {
		return nil, snapshot, err
	}
	return stream, snapshot, err
}

// demoSession keeps all proof of UI ownership inside the host.
type demoSession struct {
	sessionEvidence
	sessionStreamState
	mu               sync.Mutex
	leaseGate        sync.Mutex
	client           sessionClient
	hostID           uuid.UUID
	backendID        uuid.UUID
	databaseID       uuid.UUID
	clientID         uuid.UUID
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
	closed           bool
	closing          bool
	revision         int64
	lastStatus       contract.ConnectionStateStatus
	lastLinkValid    bool
	lastGeneration   int64
	onChange         func()
	onEvent          func(contract.Event)
	onState          func(bool, bool)
	ctx              context.Context
	cancel           context.CancelFunc
	actionsCtx       context.Context
	cancelActions    context.CancelFunc
}

type displacedAttempt struct {
	id       uuid.UUID
	identity string
	inFlight bool
}

func (s *demoSession) lookupStop(ctx context.Context, lease, testID, commandID uuid.UUID) (contract.CommandStatus, error) {
	body, err := json.Marshal(contract.StopTestCommand{CommandId: commandID})
	if err != nil {
		return contract.CommandStatus{}, err
	}
	return s.client.LookupStopExact(ctx, lease, s.databaseID, testID, commandID, body)
}

func (s *demoSession) changedLocked() {
	s.revision++
	if s.onChange != nil {
		s.onChange()
	}
}

func newDemoSession(parent context.Context, client sessionClient, hostID, backendID uuid.UUID) *demoSession {
	ctx, cancel := context.WithCancel(parent)
	actionsCtx, cancelActions := context.WithCancel(ctx)
	return &demoSession{client: client, hostID: hostID, backendID: backendID, clientID: uuid.New(), commands: make(map[uuid.UUID]contract.CommandStatus), commandPayloads: make(map[uuid.UUID]string), inFlight: make(map[uuid.UUID]bool), ambiguous: make(map[uuid.UUID]bool), knownStops: make(map[uuid.UUID]uuid.UUID), savedTests: make(map[uuid.UUID]uuid.UUID), ctx: ctx, cancel: cancel, actionsCtx: actionsCtx, cancelActions: cancelActions, revision: 1, lastStatus: contract.Disconnected}
}

func (s *demoSession) connectionState() contract.ConnectionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connectionStateLocked(time.Now())
}

type sessionFreshness struct {
	status    contract.ConnectionStateStatus
	linkValid bool
	deadline  time.Time
}

func (s *demoSession) connectionStateLocked(now time.Time) contract.ConnectionState {
	return s.publishConnectionLocked(now, s.freshnessLocked(now))
}

func (s *demoSession) publishConnectionLocked(now time.Time, freshness sessionFreshness) contract.ConnectionState {
	if freshness.status != s.lastStatus || freshness.linkValid != s.lastLinkValid || s.lastGeneration != s.streamGeneration {
		previousLink := s.lastLinkValid
		if freshness.status != contract.Live {
			s.pauseLiveLocked(now)
		} else if s.liveMark.IsZero() {
			s.liveMark = now
		}
		s.lastStatus = freshness.status
		s.lastLinkValid = freshness.linkValid
		s.lastGeneration = s.streamGeneration
		s.changedLocked()
		if s.onState != nil && previousLink != freshness.linkValid {
			s.onState(previousLink, freshness.linkValid)
		}
	}
	return contract.ConnectionState{HostRunId: s.hostID, Revision: s.revision, Status: freshness.status, LinkValid: freshness.linkValid, LeaseAcquireUnknown: s.unknownAcquire, StreamGeneration: s.streamGeneration}
}

// sessionView takes a single ordered snapshot of connection freshness and data.
func (s *demoSession) sessionView() (contract.SessionView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionViewLocked(time.Now())
}

func (s *demoSession) sessionViewLocked(now time.Time) (contract.SessionView, error) {
	freshness := s.freshnessLocked(now)
	result := contract.SessionView{Connection: s.publishConnectionLocked(now, freshness)}
	if s.snapshot != nil && !s.closed {
		copy, err := copySessionSnapshot(s.snapshot)
		if err != nil {
			return contract.SessionView{}, err
		}
		result.Snapshot = &copy
	}
	if freshness.linkValid {
		if remaining := freshness.deadline.Sub(now).Milliseconds(); remaining > 0 {
			result.LinkValidForMs = remaining
			if result.Connection.Status == contract.Live {
				result.ValidForMs = remaining
			}
		}
	}
	return result, nil
}

func (s *demoSession) acceptSnapshot(ctx context.Context, lease uuid.UUID, snapshot contract.Snapshot) error {
	if snapshot.BackendRunId != s.backendID || !s.canObserve(lease) {
		return errSessionUnavailable
	}
	s.reconcilePending(ctx, lease)
	if !s.canObserve(lease) {
		return errSessionUnavailable
	}
	s.mu.Lock()
	s.snapshot = &snapshot
	if snapshot.ActiveTest != nil {
		s.rememberStopLocked(*snapshot.ActiveTest)
	} else if s.activeTestID != uuid.Nil {
		s.activeTestID, s.activeStopID = uuid.Nil, uuid.Nil
		s.activeGeneration++
	}
	s.changedLocked()
	s.mu.Unlock()
	return nil
}

func (s *demoSession) reconcilePending(ctx context.Context, lease uuid.UUID) {
	s.mu.Lock()
	type trackedCommand struct {
		id       uuid.UUID
		identity string
	}
	pending := make([]trackedCommand, 0, len(s.commands))
	for id, command := range s.commands {
		identity := s.commandPayloads[id]
		_, reservedStop := s.reservedStopTestLocked(id)
		if !terminalCommand(command.Status) && !s.inFlight[id] && !s.ambiguous[id] {
			if !reservedStop && !strings.HasPrefix(identity, "stop:") {
				pending = append(pending, trackedCommand{id: id, identity: identity})
			}
		}
	}
	s.mu.Unlock()
	for _, item := range pending {
		requestCtx, cancel := context.WithTimeout(ctx, sessionRequestLimit)
		command, err := s.client.Command(requestCtx, lease, item.id)
		cancel()
		if err == nil {
			s.mu.Lock()
			_, reservedStop := s.reservedStopTestLocked(item.id)
			identityMatches := !reservedStop
			if existing, ok := s.commands[item.id]; ok && identityMatches && s.commandPayloads[item.id] == item.identity && !s.inFlight[item.id] && !s.ambiguous[item.id] {
				s.commands[item.id] = mergeCommandStatus(existing, command)
			}
			s.mu.Unlock()
		}
	}
}

func terminalCommand(status contract.CommandStatusStatus) bool {
	return status == contract.CommandStatusStatusCompleted || status == contract.CommandStatusStatusRejected || status == contract.CommandStatusStatusFailed
}

// createCoveredLocked requires a later snapshot or a confirmed saved record.
// Older snapshots can arrive after the command response and cannot establish its outcome.
func (s *demoSession) createCoveredLocked(id uuid.UUID, status contract.CommandStatus, snapshot *contract.Snapshot) bool {
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
	return snapshot != nil && snapshot.BackendRunId == s.backendID && snapshot.Revision > command.ExpectedRevision
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

func (s *demoSession) snapshotCopy() (contract.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot == nil || s.closed {
		return contract.Snapshot{}, errSessionUnavailable
	}
	return copySessionSnapshot(s.snapshot)
}

func copySessionSnapshot(snapshot *contract.Snapshot) (contract.Snapshot, error) {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return contract.Snapshot{}, errSessionUnavailable
	}
	var result contract.Snapshot
	if err := json.Unmarshal(data, &result); err != nil {
		return contract.Snapshot{}, errSessionUnavailable
	}
	return result, nil
}

func (s *demoSession) rememberStopLocked(record contract.TestRecord) {
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

func (s *demoSession) rememberSavedLocked(record contract.TestRecord) {
	if record.TestId == uuid.Nil || record.StopCommandId == uuid.Nil || record.Stage != "finished" || record.SaveStatus != "saved" {
		return
	}
	if s.activeTestID == record.TestId && s.activeStopID != record.StopCommandId {
		return
	}
	matched := s.activeTestID == record.TestId && s.activeStopID == record.StopCommandId
	if s.snapshot != nil && s.snapshot.ActiveTest != nil {
		current := s.snapshot.ActiveTest
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
			referenced := s.activeTestID == testID || (s.snapshot != nil && s.snapshot.ActiveTest != nil && s.snapshot.ActiveTest.TestId == testID)
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

func (s *demoSession) savedRecordLocked(record *contract.TestRecord) *contract.TestRecord {
	if record == nil || record.TestId == uuid.Nil || record.StopCommandId == uuid.Nil || s.savedTests[record.TestId] != record.StopCommandId {
		return record
	}
	copy := *record
	copy.Stage, copy.SaveStatus = "finished", "saved"
	return &copy
}

func (s *demoSession) rememberStopPairLocked(record contract.TestRecord) {
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

func (s *demoSession) reservedStopTestLocked(id uuid.UUID) (uuid.UUID, bool) {
	if id != uuid.Nil && id == s.activeStopID && s.activeTestID != uuid.Nil {
		return s.activeTestID, true
	}
	if s.snapshot != nil && s.snapshot.ActiveTest != nil {
		record := s.snapshot.ActiveTest
		if id != uuid.Nil && id == record.StopCommandId && record.TestId != uuid.Nil {
			return record.TestId, true
		}
	}
	testID, exists := s.knownStops[id]
	return testID, exists
}

func (s *demoSession) stopStatusLocked(id uuid.UUID) (contract.CommandStatus, bool) {
	current, tracked := s.commands[id]
	identity := s.commandPayloads[id]
	if testID, reserved := s.reservedStopTestLocked(id); reserved {
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

func (s *demoSession) commandAllowedLocked(stop bool) bool {
	return s.commandAllowedAtLocked(stop, time.Now())
}

func (s *demoSession) commandAllowedAtLocked(stop bool, now time.Time) bool {
	if stop {
		return !s.closed && !s.closing && s.leaseID != uuid.Nil
	}
	return !s.ordinaryCommandBlockedLocked() && s.freshnessLocked(now).status == contract.Live
}

func (s *demoSession) ordinaryCommandBlockedLocked() bool {
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

// command registers the identifier before sending and never resends it automatically.
func (s *demoSession) command(ctx context.Context, id uuid.UUID, identity string, stop bool, send func(context.Context, uuid.UUID) (contract.CommandStatus, error), invalidNew ...error) (result contract.CommandStatus, resultErr error) {
	if id == uuid.Nil {
		return contract.CommandStatus{}, errSessionUnavailable
	}
	s.mu.Lock()
	if !stop {
		if _, reserved := s.reservedStopTestLocked(id); reserved {
			s.mu.Unlock()
			return contract.CommandStatus{}, ErrCommandConflict
		}
	}
	if existing, ok := s.commands[id]; ok {
		if s.commandPayloads[id] != identity {
			if !stop {
				s.mu.Unlock()
				return contract.CommandStatus{}, ErrCommandConflict
			}
			if !s.commandAllowedLocked(true) {
				s.mu.Unlock()
				return contract.CommandStatus{}, errSessionUnavailable
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
				s.mu.Unlock()
				return existing, nil
			}
		}
	}
	valid := s.commandAllowedLocked(stop)
	limit := 64
	if stop {
		limit++
	}
	if len(s.commands) >= limit {
		for previous, status := range s.commands {
			activeStop := previous == s.activeStopID || (s.snapshot != nil && s.snapshot.ActiveTest != nil && previous == s.snapshot.ActiveTest.StopCommandId)
			if terminalCommand(status.Status) && !s.inFlight[previous] && (!activeStop || !strings.HasPrefix(s.commandPayloads[previous], "stop:")) && s.createCoveredLocked(previous, status, s.snapshot) {
				delete(s.commands, previous)
				delete(s.commandPayloads, previous)
				delete(s.ambiguous, previous)
				break
			}
		}
	}
	if !valid || len(s.commands) >= limit {
		s.mu.Unlock()
		return contract.CommandStatus{}, errors.Join(errSessionUnavailable, errCommandNotSent)
	}
	lease := s.leaseID
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
	actionsCtx := s.actionsCtx
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.displaced != nil && s.displaced.id == id && s.displaced.identity == identity {
			var failure *transport.RequestError
			if errors.Is(resultErr, errCommandNotSent) || (errors.As(resultErr, &failure) && !failure.OutcomeUnknown) {
				s.displaced = nil
			} else {
				s.displaced.inFlight = false
			}
		}
		s.mu.Unlock()
	}()
	requestCtx, requestCancel := context.WithCancel(ctx)
	defer requestCancel()
	if !stop {
		cancelOnClose := context.AfterFunc(actionsCtx, requestCancel)
		defer cancelOnClose()
	}
	previouslyPresent := false
	var streamDone <-chan struct{}
	var admissionDeadline time.Time
	if !stop {
		previous, lookupErr := s.client.Command(requestCtx, lease, id)
		if lookupErr == nil {
			if previous.Status == contract.CommandStatusStatusUnknown {
				s.mu.Lock()
				if s.commandPayloads[id] == identity {
					delete(s.commands, id)
					delete(s.commandPayloads, id)
					delete(s.inFlight, id)
				}
				s.mu.Unlock()
				return contract.CommandStatus{}, errors.New("command identifier is already unresolved")
			}
			previouslyPresent = true
		} else {
			var failure *transport.RequestError
			if !errors.As(lookupErr, &failure) || failure.Status != 404 || failure.Code != "command_not_found" {
				s.mu.Lock()
				if s.commandPayloads[id] == identity {
					delete(s.commands, id)
					delete(s.commandPayloads, id)
					delete(s.inFlight, id)
				}
				s.mu.Unlock()
				return contract.CommandStatus{}, lookupErr
			}
		}
		if !previouslyPresent && len(invalidNew) != 0 && invalidNew[0] != nil {
			s.mu.Lock()
			if s.commandPayloads[id] == identity {
				delete(s.commands, id)
				delete(s.commandPayloads, id)
				delete(s.inFlight, id)
			}
			s.mu.Unlock()
			return contract.CommandStatus{}, errors.Join(invalidNew[0], errCommandNotSent)
		}
		s.mu.Lock()
		admitted := false
		if s.commandPayloads[id] == identity && s.leaseID == lease && !s.ordinaryCommandBlockedLocked() {
			freshness := s.freshnessLocked(time.Now())
			admitted = freshness.status == contract.Live && requestCtx.Err() == nil
			if admitted {
				admissionDeadline = freshness.deadline
				streamDone = s.streamDone
			}
		}
		if !admitted && s.commandPayloads[id] == identity {
			delete(s.commands, id)
			delete(s.commandPayloads, id)
			delete(s.inFlight, id)
		}
		s.mu.Unlock()
		if !admitted {
			if !previouslyPresent {
				return contract.CommandStatus{}, errors.Join(errSessionUnavailable, errCommandNotSent)
			}
			return contract.CommandStatus{}, errSessionUnavailable
		}
	}
	sendCtx := requestCtx
	if !stop {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithDeadline(requestCtx, admissionDeadline)
		defer cancel()
		go func() {
			select {
			case <-streamDone:
				cancel()
			case <-sendCtx.Done():
			}
		}()
	}
	result, err := send(sendCtx, lease)
	s.mu.Lock()
	defer s.mu.Unlock()
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

func (s *demoSession) close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.lastProbe = time.Time{}
		s.changedLocked()
		_ = s.connectionStateLocked(time.Now())
		s.cancel()
		s.cancelActions()
	}
	s.mu.Unlock()
}

func stopOutcomeUnconfirmed(active *contract.TestRecord, id uuid.UUID, status contract.CommandStatus) bool {
	if active == nil || active.Stage != "finished" || id != active.StopCommandId || status.Status == contract.CommandStatusStatusCompleted {
		return false
	}
	return status.Status != contract.CommandStatusStatusRejected || (active.Outcome != "completed" && active.Outcome != "interrupted")
}

// shutdown attempts a single stop and confirms its result before releasing control.
func (s *demoSession) shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closing = true
		s.cancelActions()
		s.changedLocked()
		_ = s.connectionStateLocked(time.Now())
	}
	leasePending := !s.leaseGate.TryLock()
	if !leasePending {
		s.leaseGate.Unlock()
	}
	lease := s.leaseID
	unknownAcquire := s.unknownAcquire
	var active *contract.TestRecord
	if s.snapshot != nil && s.snapshot.ActiveTest != nil {
		copy := *s.snapshot.ActiveTest
		active = &copy
	}
	if s.activeTestID != uuid.Nil && s.activeStopID != uuid.Nil && (active == nil || active.TestId != s.activeTestID) {
		active = &contract.TestRecord{TestId: s.activeTestID, StopCommandId: s.activeStopID, Stage: "running"}
	}
	active = s.savedRecordLocked(active)
	unconfirmed := unknownAcquire || leasePending || s.cleanupUnknown || s.displaced != nil
	if active != nil && (active.Stage != "finished" || active.SaveStatus != "saved") {
		unconfirmed = true
	}
	if s.activeTestID != uuid.Nil && (active == nil || active.TestId != s.activeTestID) {
		unconfirmed = true
	}
	for id, status := range s.commands {
		if !terminalCommand(status.Status) || !s.createCoveredLocked(id, status, s.snapshot) || stopOutcomeUnconfirmed(active, id, status) {
			unconfirmed = true
			break
		}
	}
	closed := s.closed
	s.mu.Unlock()
	if closed || lease == uuid.Nil {
		if unconfirmed {
			return ErrSessionShutdownUnconfirmed
		}
		return nil
	}
	if active != nil && active.Stage != "finished" {
		s.startShutdownStop(ctx, lease, active)
	}
	fresh, snapshotErr := s.client.Snapshot(ctx, lease)
	snapshotKnown := snapshotErr == nil && fresh.BackendRunId == s.backendID
	if snapshotKnown {
		s.mu.Lock()
		active = s.savedRecordLocked(fresh.ActiveTest)
		s.mu.Unlock()
		if active != nil && active.Stage != "finished" {
			s.startShutdownStop(ctx, lease, active)
		}
	}
	unknown := unknownAcquire || leasePending
	s.mu.Lock()
	pending := make([]uuid.UUID, 0, len(s.commands))
	for id, status := range s.commands {
		if !terminalCommand(status.Status) {
			pending = append(pending, id)
		} else if stopOutcomeUnconfirmed(active, id, status) {
			unknown = true
		}
	}
	s.mu.Unlock()
	confirmFinishedStop := false
	var startsAfterSave []uuid.UUID
	for _, id := range pending {
		s.mu.Lock()
		unresolved := s.inFlight[id] || s.ambiguous[id]
		identity := s.commandPayloads[id]
		s.mu.Unlock()
		if unresolved {
			stopForActive := active != nil && id == active.StopCommandId
			if stopForActive && active.Stage == "finished" {
				stopIdentity, err := commandIdentity("stop:"+active.TestId.String(), contract.StopTestCommand{CommandId: id})
				if err == nil && identity == stopIdentity {
					confirmFinishedStop = true
				} else {
					unknown = true
				}
			} else if !stopForActive {
				unknown = true
			}
			continue
		}
		var status contract.CommandStatus
		var err error
		if strings.HasPrefix(identity, "stop:") {
			s.mu.Lock()
			tracked := s.commands[id]
			s.mu.Unlock()
			if tracked.TestId == nil {
				err = errSessionUnavailable
			} else {
				status, err = s.lookupStop(ctx, lease, *tracked.TestId, id)
			}
		} else {
			status, err = s.client.Command(ctx, lease, id)
		}
		s.mu.Lock()
		if existing, ok := s.commands[id]; ok && s.commandPayloads[id] == identity && !s.inFlight[id] && !s.ambiguous[id] {
			if err == nil {
				s.commands[id] = mergeCommandStatus(existing, status)
			}
			if terminalCommand(s.commands[id].Status) {
				status, err = s.commands[id], nil
			}
		} else {
			err = errSessionUnavailable
		}
		s.mu.Unlock()
		stopForActive := active != nil && id == active.StopCommandId
		if stopForActive && active.Stage == "finished" {
			if err != nil || status.Status != contract.CommandStatusStatusCompleted {
				if active.SaveStatus != "saved" {
					confirmFinishedStop = true
				} else {
					unknown = true
				}
			}
		} else if (err != nil || !terminalCommand(status.Status)) && !stopForActive {
			if active != nil && (active.Stage != "finished" || active.SaveStatus != "saved") && strings.HasPrefix(identity, "start:"+active.TestId.String()+":") {
				startsAfterSave = append(startsAfterSave, id)
			} else {
				unknown = true
			}
		}
	}
	s.mu.Lock()
	for id, status := range s.commands {
		if !s.createCoveredLocked(id, status, &fresh) {
			unknown = true
		}
	}
	s.mu.Unlock()
	var result error
	s.mu.Lock()
	cleanupUnknown := s.cleanupUnknown || s.displaced != nil
	s.mu.Unlock()
	if unknown || !snapshotKnown || cleanupUnknown {
		result = ErrSessionShutdownUnconfirmed
	}
	if active != nil && (active.Stage != "finished" || confirmFinishedStop) {
		stopID := active.StopCommandId
		stopIdentity, identityErr := commandIdentity("stop:"+active.TestId.String(), contract.StopTestCommand{CommandId: stopID})
		if identityErr != nil {
			return ErrSessionShutdownUnconfirmed
		}
		if active.Stage != "finished" {
			s.startShutdownStop(ctx, lease, active)
		}
		for {
			s.mu.Lock()
			cached, tracked := s.commands[stopID]
			tracked = tracked && s.commandPayloads[stopID] == stopIdentity
			s.mu.Unlock()
			if tracked && cached.Status == contract.CommandStatusStatusCompleted {
				break
			}
			status, err := s.lookupStop(ctx, lease, active.TestId, stopID)
			if err == nil && status.Status == contract.CommandStatusStatusCompleted {
				break
			}
			if err == nil && (status.Status == contract.CommandStatusStatusFailed || status.Status == contract.CommandStatusStatusRejected) {
				result = ErrSessionShutdownUnconfirmed
				break
			}
			select {
			case <-ctx.Done():
				return ErrSessionShutdownUnconfirmed
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	if active != nil && (active.Stage != "finished" || active.SaveStatus != "saved") {
		for {
			record, err := s.client.Test(ctx, lease, active.TestId)
			if err == nil && record.Stage == "finished" && record.SaveStatus == "saved" {
				break
			}
			select {
			case <-ctx.Done():
				return ErrSessionShutdownUnconfirmed
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	for _, id := range startsAfterSave {
		status, err := s.client.Command(ctx, lease, id)
		s.mu.Lock()
		existing, tracked := s.commands[id]
		if tracked && !s.inFlight[id] && !s.ambiguous[id] && err == nil {
			s.commands[id] = mergeCommandStatus(existing, status)
			status = s.commands[id]
		} else {
			err = ErrSessionShutdownUnconfirmed
		}
		s.mu.Unlock()
		if err != nil || !terminalCommand(status.Status) || status.TestId == nil || *status.TestId != active.TestId {
			result = ErrSessionShutdownUnconfirmed
		}
	}
	if err := s.client.ReleaseLease(ctx, lease); err != nil {
		result = ErrSessionShutdownUnconfirmed
	}
	return result
}

func (s *demoSession) startShutdownStop(ctx context.Context, lease uuid.UUID, active *contract.TestRecord) {
	stopID := active.StopCommandId
	command := contract.StopTestCommand{CommandId: stopID}
	identity, err := commandIdentity("stop:"+active.TestId.String(), command)
	if err != nil {
		return
	}
	s.mu.Lock()
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
	s.mu.Unlock()
	if tracked {
		return
	}
	status, err := s.client.StopTest(ctx, lease, active.TestId, command)
	s.mu.Lock()
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
	s.mu.Unlock()
}

// freshnessLocked observes the stream once under the session mutex.
func (s *demoSession) freshnessLocked(now time.Time) sessionFreshness {
	return s.freshness(now, s.closed || s.closing, &s.sessionStreamState)
}
func (s *demoSession) pauseLiveLocked(now time.Time) {
	if s.liveMark.IsZero() {
		return
	}
	var pending time.Time
	if s.stream != nil {
		pending = s.stream.PendingSince()
	}
	s.pauseLive(now, pending)
}
