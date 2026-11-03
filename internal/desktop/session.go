package desktop

import (
	"context"
	"encoding/json"
	"errors"
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
	hostLedger
	sessionStreamState
	mu             sync.Mutex
	leaseGate      sync.Mutex
	client         sessionClient
	hostID         uuid.UUID
	backendID      uuid.UUID
	databaseID     uuid.UUID
	clientID       uuid.UUID
	closed         bool
	closing        bool
	revision       int64
	lastStatus     contract.ConnectionStateStatus
	lastLinkValid  bool
	lastGeneration int64
	onChange       func()
	onEvent        func(contract.Event)
	onState        func(bool, bool)
	ctx            context.Context
	cancel         context.CancelFunc
	actionsCtx     context.Context
	cancelActions  context.CancelFunc
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
	return &demoSession{client: client, hostID: hostID, backendID: backendID, clientID: uuid.New(), hostLedger: newHostLedger(), ctx: ctx, cancel: cancel, actionsCtx: actionsCtx, cancelActions: cancelActions, revision: 1, lastStatus: contract.Disconnected}
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
	s.observeSnapshot(snapshot)
	s.changedLocked()
	s.mu.Unlock()
	return nil
}

func (s *demoSession) reconcilePending(ctx context.Context, lease uuid.UUID) {
	s.mu.Lock()
	pending := s.pendingOrdinary(s.snapshot)
	s.mu.Unlock()
	for _, item := range pending {
		requestCtx, cancel := context.WithTimeout(ctx, sessionRequestLimit)
		command, err := s.client.Command(requestCtx, lease, item.id)
		cancel()
		if err == nil {
			s.mu.Lock()
			s.applyOrdinaryObservation(item, command, s.snapshot)
			s.mu.Unlock()
		}
	}
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

func (s *demoSession) commandAllowedLocked(stop bool) bool {
	return s.commandAllowedAtLocked(stop, time.Now())
}

func (s *demoSession) commandAllowedAtLocked(stop bool, now time.Time) bool {
	if stop {
		return !s.closed && !s.closing && s.leaseID != uuid.Nil
	}
	return !s.ordinaryCommandBlockedLocked() && s.freshnessLocked(now).status == contract.Live
}

// command registers the identifier before sending and never resends it automatically.
func (s *demoSession) command(ctx context.Context, id uuid.UUID, identity string, stop bool, send func(context.Context, uuid.UUID) (contract.CommandStatus, error), invalidNew ...error) (result contract.CommandStatus, resultErr error) {
	if id == uuid.Nil {
		return contract.CommandStatus{}, errSessionUnavailable
	}
	s.mu.Lock()
	previous, done, err := s.prepareAttempt(id, identity, stop, s.commandAllowedLocked(true), s.snapshot)
	if done {
		s.mu.Unlock()
		return previous, err
	}
	if err := s.registerAttempt(id, identity, stop, s.commandAllowedLocked(stop), s.snapshot, s.backendID); err != nil {
		s.mu.Unlock()
		return contract.CommandStatus{}, err
	}
	lease := s.leaseID
	actionsCtx := s.actionsCtx
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.finishDisplaced(id, identity, resultErr)
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
				s.discardPrepared(id, identity)
				s.mu.Unlock()
				return contract.CommandStatus{}, errors.New("command identifier is already unresolved")
			}
			previouslyPresent = true
		} else {
			var failure *transport.RequestError
			if !errors.As(lookupErr, &failure) || failure.Status != 404 || failure.Code != "command_not_found" {
				s.mu.Lock()
				s.discardPrepared(id, identity)
				s.mu.Unlock()
				return contract.CommandStatus{}, lookupErr
			}
		}
		if !previouslyPresent && len(invalidNew) != 0 && invalidNew[0] != nil {
			s.mu.Lock()
			s.discardPrepared(id, identity)
			s.mu.Unlock()
			return contract.CommandStatus{}, errors.Join(invalidNew[0], errCommandNotSent)
		}
		s.mu.Lock()
		admitted := false
		if s.matchesAttempt(id, identity) && s.leaseID == lease && !s.ordinaryCommandBlockedLocked() {
			freshness := s.freshnessLocked(time.Now())
			admitted = freshness.status == contract.Live && requestCtx.Err() == nil
			if admitted {
				admissionDeadline = freshness.deadline
				streamDone = s.streamDone
			}
		}
		if !admitted && s.matchesAttempt(id, identity) {
			s.discardPrepared(id, identity)
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
	result, err = send(sendCtx, lease)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finishAttempt(id, identity, stop, previouslyPresent, result, err)
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

func (s *demoSession) createCoveredLocked(id uuid.UUID, status contract.CommandStatus, snapshot *contract.Snapshot) bool {
	return s.createCovered(id, status, snapshot, s.backendID)
}
