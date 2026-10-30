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
	StopTest(context.Context, uuid.UUID, uuid.UUID, contract.StopTestCommand) (contract.CommandStatus, error)
	StopTestRaw(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, []byte) (contract.CommandStatus, error)
	openStream(context.Context, uuid.UUID, uuid.UUID) (sessionStream, contract.Snapshot, error)
}

type nativeSessionClient struct{ *transport.Client }

func (c nativeSessionClient) openStream(ctx context.Context, lease, runID uuid.UUID) (sessionStream, contract.Snapshot, error) {
	return c.OpenStream(ctx, lease, runID)
}

// demoSession keeps all proof of UI ownership inside the host.
type demoSession struct {
	mu               sync.Mutex
	leaseGate        sync.Mutex
	client           sessionClient
	hostID           uuid.UUID
	backendID        uuid.UUID
	clientID         uuid.UUID
	leaseID          uuid.UUID
	leaseUntil       time.Time
	leaseConfirmed   bool
	lastUI           time.Time
	lastProbe        time.Time
	liveMark         time.Time
	liveDuration     time.Duration
	recoverySince    time.Time
	snapshot         *contract.Snapshot
	snapshotPending  bool
	commands         map[uuid.UUID]contract.CommandStatus
	commandPayloads  map[uuid.UUID]string
	inFlight         map[uuid.UUID]bool
	ambiguous        map[uuid.UUID]bool
	knownStops       map[uuid.UUID]uuid.UUID
	activeTestID     uuid.UUID
	activeStopID     uuid.UUID
	activeGeneration uint64
	savedTests       map[uuid.UUID]uuid.UUID
	unknownAcquire   bool
	streamActive     bool
	streamGeneration int64
	streamDone       <-chan struct{}
	stream           sessionStream
	streamStopped    chan struct{}
	streamBroken     bool
	cleanupUnknown   bool
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

// keepAlive is triggered by the mounted UI, not by a host timer or WebSocket.
func (s *demoSession) keepAlive(ctx context.Context) error {
	return s.keepAliveUntil(ctx, time.Time{})
}

func (s *demoSession) keepAliveUntil(ctx context.Context, admissionDeadline time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed || s.closing || ctx.Err() != nil || (!admissionDeadline.IsZero() && !time.Now().Before(admissionDeadline)) {
		s.mu.Unlock()
		return errSessionUnavailable
	}
	if s.unknownAcquire || (s.streamBroken && (s.cleanupUnknown || s.streamActive || s.leaseID == uuid.Nil)) {
		s.mu.Unlock()
		return errSessionUnavailable
	}
	s.mu.Unlock()
	if !s.leaseGate.TryLock() {
		return errSessionBusy
	}
	defer s.leaseGate.Unlock()
	s.mu.Lock()
	started := time.Now()
	if s.closed || s.closing || ctx.Err() != nil || (!admissionDeadline.IsZero() && !started.Before(admissionDeadline)) || s.unknownAcquire || (s.streamBroken && (s.cleanupUnknown || s.streamActive || s.leaseID == uuid.Nil)) {
		s.mu.Unlock()
		return errSessionUnavailable
	}
	s.pauseLiveLocked(started)
	previousLease := s.leaseID
	previousUntil := s.leaseUntil
	continuous := s.leaseConfirmed && previousLease != uuid.Nil && started.Before(previousUntil) && !s.lastUI.IsZero() && started.Before(s.lastUI.Add(uiHeartbeatFreshness))
	s.lastUI = started
	if !s.recoverySince.IsZero() && started.Sub(s.recoverySince) >= sessionRecoveryLimit {
		s.streamBroken = true
	}
	if s.streamBroken && (s.cleanupUnknown || s.streamActive || s.leaseID == uuid.Nil) {
		s.mu.Unlock()
		return errSessionUnavailable
	}
	lease := s.leaseID
	broken := s.streamBroken
	requestCtx, cancel := context.WithDeadline(ctx, started.Add(sessionRequestLimit))
	s.mu.Unlock()
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() { stop(); cancel() }()
	if err := requestCtx.Err(); err != nil {
		return err
	}
	var granted contract.UiLease
	var err error
	if broken {
		_, err = s.client.AcquireLease(requestCtx, s.clientID, &lease)
		s.mu.Lock()
		defer s.mu.Unlock()
		var failure *transport.RequestError
		if errors.As(err, &failure) && failure.Status == 403 && failure.Code == "lease_invalid" && !s.closed && !s.closing && s.leaseID == lease {
			s.leaseID = uuid.Nil
			s.leaseUntil = time.Time{}
			s.leaseConfirmed = false
			s.streamBroken = false
			s.recoverySince = time.Time{}
			s.liveDuration = 0
			s.changedLocked()
		}
		return errSessionUnavailable
	}
	if lease == uuid.Nil {
		granted, err = s.client.AcquireLease(requestCtx, s.clientID, nil)
	} else {
		granted, err = s.client.RenewLease(requestCtx, lease)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.closing {
		return errSessionUnavailable
	}
	if err != nil {
		var failure *transport.RequestError
		if lease == uuid.Nil && errors.As(err, &failure) && failure.OutcomeUnknown {
			s.unknownAcquire = true
		}
		if lease != uuid.Nil && errors.As(err, &failure) && failure.Status == 403 && failure.Code == "lease_invalid" {
			s.leaseID = uuid.Nil
			s.leaseUntil = time.Time{}
		}
		s.lastProbe = time.Time{}
		s.leaseConfirmed = false
		s.invalidateActionsLocked()
		s.changedLocked()
		_ = s.connectionStateLocked(time.Now())
		return err
	}
	if granted.LeaseId == uuid.Nil || granted.ClientId != s.clientID || (lease != uuid.Nil && granted.LeaseId != lease) {
		if lease == uuid.Nil {
			s.unknownAcquire = true
		}
		s.lastProbe = time.Time{}
		s.leaseConfirmed = false
		s.invalidateActionsLocked()
		s.changedLocked()
		_ = s.connectionStateLocked(time.Now())
		return errSessionUnavailable
	}
	s.leaseID = granted.LeaseId
	s.leaseConfirmed = true
	s.leaseUntil = started.Add(leaseDuration)
	if granted.ExpiresAt.Before(s.leaseUntil) {
		s.leaseUntil = granted.ExpiresAt
	}
	if !continuous || previousLease != granted.LeaseId || s.leaseUntil.Before(previousUntil) {
		s.changedLocked()
	}
	if err := requestCtx.Err(); err != nil {
		s.leaseConfirmed = false
		s.lastProbe = time.Time{}
		s.invalidateActionsLocked()
		s.changedLocked()
		_ = s.connectionStateLocked(time.Now())
		return err
	}
	if time.Since(s.lastUI) >= uiHeartbeatFreshness || !time.Now().Before(s.leaseUntil) {
		return errSessionUnavailable
	}
	if !s.streamActive {
		s.streamActive = true
		s.snapshotPending = true
		if s.recoverySince.IsZero() {
			s.recoverySince = time.Now()
		}
		s.streamStopped = make(chan struct{})
		go s.runStream(granted.LeaseId)
	}
	s.startLiveLocked(time.Now())
	return nil
}

func (s *demoSession) connectionState() contract.ConnectionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connectionStateLocked(time.Now())
}

func (s *demoSession) connectionStateLocked(now time.Time) contract.ConnectionState {
	status := contract.Disconnected
	linkValid := false
	if !s.closed && !s.closing && !s.unknownAcquire && !s.streamBroken && s.leaseConfirmed && !s.lastUI.IsZero() && now.Sub(s.lastUI) < uiHeartbeatFreshness && now.Before(s.leaseUntil) {
		status = contract.Recovering
		streamOpen := s.streamActive && s.streamDone != nil
		if streamOpen {
			select {
			case <-s.streamDone:
				streamOpen = false
			default:
			}
		}
		linkValid = streamOpen && s.stream != nil && !s.lastProbe.IsZero() && now.Sub(s.lastProbe) < streamProbeFreshness
		if linkValid && !s.stream.Pending() && !s.snapshotPending && s.snapshot != nil {
			status = contract.Live
		}
	}
	if status != s.lastStatus || linkValid != s.lastLinkValid || s.lastGeneration != s.streamGeneration {
		previousLink := s.lastLinkValid
		if status != contract.Live {
			s.pauseLiveLocked(now)
		} else {
			s.startLiveLocked(now)
		}
		s.lastStatus = status
		s.lastLinkValid = linkValid
		s.lastGeneration = s.streamGeneration
		s.changedLocked()
		if s.onState != nil && previousLink != linkValid {
			s.onState(previousLink, linkValid)
		}
	}
	return contract.ConnectionState{HostRunId: s.hostID, Revision: s.revision, Status: status, LinkValid: linkValid, LeaseAcquireUnknown: s.unknownAcquire, StreamGeneration: s.streamGeneration}
}

// sessionView takes a single ordered snapshot of connection freshness and data.
func (s *demoSession) sessionView() (contract.SessionView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	result := contract.SessionView{Connection: s.connectionStateLocked(now)}
	if s.snapshot != nil && !s.closed {
		copy, err := copySessionSnapshot(s.snapshot)
		if err != nil {
			return contract.SessionView{}, err
		}
		result.Snapshot = &copy
	}
	if result.Connection.LinkValid {
		until := s.lastUI.Add(uiHeartbeatFreshness)
		if s.leaseUntil.Before(until) {
			until = s.leaseUntil
		}
		if probeUntil := s.lastProbe.Add(streamProbeFreshness); probeUntil.Before(until) {
			until = probeUntil
		}
		if remaining := until.Sub(now).Milliseconds(); remaining > 0 {
			result.LinkValidForMs = remaining
			if result.Connection.Status == contract.Live {
				result.ValidForMs = remaining
			}
		}
	}
	return result, nil
}

func (s *demoSession) canObserve(lease uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	return !s.closed && !s.closing && !s.streamBroken && s.leaseConfirmed && s.leaseID == lease && now.Before(s.leaseUntil) && !s.lastUI.IsZero() && now.Sub(s.lastUI) < uiHeartbeatFreshness
}

func (s *demoSession) runStream(lease uuid.UUID) {
	defer func() {
		s.mu.Lock()
		s.streamActive = false
		s.streamDone = nil
		s.stream = nil
		s.snapshotPending = true
		s.lastProbe = time.Time{}
		s.changedLocked()
		close(s.streamStopped)
		s.mu.Unlock()
	}()
	for attempt := 0; attempt < 3 && s.canObserve(lease); attempt++ {
		streamCtx, cancel := context.WithCancel(s.ctx)
		timer := time.AfterFunc(sessionRequestLimit, cancel)
		stream, snapshot, err := s.client.openStream(streamCtx, lease, s.backendID)
		started := timer.Stop()
		if err == nil && (!started || streamCtx.Err() != nil) {
			err = errSessionUnavailable
		}
		if err == nil {
			s.mu.Lock()
			s.streamDone = stream.Done()
			s.stream = stream
			s.mu.Unlock()
			err = s.consumeStream(streamCtx, lease, stream, snapshot)
		}
		cancel()
		if stream != nil {
			s.mu.Lock()
			s.streamGeneration++
			s.pauseLiveLocked(time.Now())
			s.snapshotPending = true
			s.lastProbe = time.Time{}
			s.changedLocked()
			_ = s.connectionStateLocked(time.Now())
			s.mu.Unlock()
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 4*time.Second)
			closeErr := stream.Close(closeCtx)
			closeCancel()
			if closeErr != nil {
				s.mu.Lock()
				s.streamBroken = true
				s.cleanupUnknown = true
				s.mu.Unlock()
				return
			}
		}
		s.mu.Lock()
		s.pauseLiveLocked(time.Now())
		stable := s.recoverySince.IsZero()
		if s.recoverySince.IsZero() {
			s.recoverySince = time.Now()
		}
		s.streamDone = nil
		s.stream = nil
		s.lastProbe = time.Time{}
		s.changedLocked()
		s.mu.Unlock()
		if stable {
			attempt = -1
		}
		if errors.Is(err, transport.ErrStreamCloseUnconfirmed) {
			s.mu.Lock()
			s.streamBroken = true
			s.cleanupUnknown = true
			s.mu.Unlock()
			return
		}
		if !s.canObserve(lease) {
			return
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
	if s.canObserve(lease) {
		s.mu.Lock()
		s.streamBroken = true
		s.changedLocked()
		s.mu.Unlock()
	}
}

func (s *demoSession) consumeStream(ctx context.Context, lease uuid.UUID, stream sessionStream, snapshot contract.Snapshot) (result error) {
	syncCtx, syncCancel := context.WithTimeout(ctx, sessionSyncLimit)
	err := s.acceptSnapshot(syncCtx, lease, snapshot)
	if err == nil {
		err = s.drainStream(syncCtx, lease, stream)
	}
	syncCancel()
	if err != nil {
		return err
	}
	workerCtx, cancelWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	probeError := make(chan error, 1)
	go func() {
		defer close(workerDone)
		for workerCtx.Err() == nil {
			probeAt := time.Now()
			probeCtx, cancel := context.WithTimeout(workerCtx, sessionSyncLimit)
			err := stream.Probe(probeCtx)
			cancel()
			if err != nil {
				if workerCtx.Err() == nil {
					s.mu.Lock()
					if s.stream == stream && s.leaseID == lease {
						s.lastProbe = time.Time{}
						s.snapshotPending = true
						s.changedLocked()
						_ = s.connectionStateLocked(time.Now())
					}
					s.mu.Unlock()
					probeError <- err
					cancelWorker()
				}
				return
			}
			s.mu.Lock()
			valid := !s.closed && !s.closing && s.stream == stream && s.leaseID == lease && workerCtx.Err() == nil
			select {
			case <-stream.Done():
				valid = false
			default:
			}
			if valid {
				s.lastProbe = probeAt
				s.startLiveLocked(time.Now())
				_ = s.connectionStateLocked(time.Now())
			}
			s.mu.Unlock()
			if !valid {
				return
			}
			wait := time.Until(probeAt.Add(streamProbeInterval))
			if wait < 0 {
				wait = 0
			}
			timer := time.NewTimer(wait)
			select {
			case <-workerCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	defer func() {
		cancelWorker()
		select {
		case <-workerDone:
		case <-time.After(4 * time.Second):
			result = transport.ErrStreamCloseUnconfirmed
		}
	}()
	for s.canObserve(lease) {
		nextCtx, cancel := context.WithTimeout(workerCtx, time.Second)
		event, err := stream.Next(nextCtx)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			s.connectionState()
			continue
		}
		if err != nil {
			select {
			case failure := <-probeError:
				return failure
			default:
			}
			return err
		}
		s.mu.Lock()
		s.pauseLiveLocked(time.Now())
		s.snapshotPending = true
		if s.onEvent != nil {
			s.onEvent(event)
		}
		s.changedLocked()
		s.mu.Unlock()
		syncCtx, syncCancel := context.WithTimeout(workerCtx, sessionSyncLimit)
		snapshot, err := readSessionSnapshot(syncCtx, stream)
		if err != nil {
			syncCancel()
			return err
		}
		if err := s.acceptSnapshot(syncCtx, lease, snapshot); err != nil {
			syncCancel()
			return err
		}
		err = s.drainStream(syncCtx, lease, stream)
		syncCancel()
		if err != nil {
			return err
		}
	}
	return errSessionUnavailable
}

func (s *demoSession) drainStream(ctx context.Context, lease uuid.UUID, stream sessionStream) error {
	s.mu.Lock()
	s.pauseLiveLocked(time.Now())
	s.snapshotPending = true
	s.changedLocked()
	s.mu.Unlock()
	for {
		event, present, err := stream.Poll(ctx)
		if err != nil {
			return err
		}
		if !present {
			s.mu.Lock()
			s.snapshotPending = false
			s.startLiveLocked(time.Now())
			s.changedLocked()
			s.mu.Unlock()
			return nil
		}
		s.mu.Lock()
		if s.onEvent != nil {
			s.onEvent(event)
		}
		s.changedLocked()
		s.mu.Unlock()
		snapshot, err := readSessionSnapshot(ctx, stream)
		if err != nil {
			return err
		}
		if err := s.acceptSnapshot(ctx, lease, snapshot); err != nil {
			return err
		}
	}
}

func readSessionSnapshot(ctx context.Context, stream sessionStream) (contract.Snapshot, error) {
	for {
		snapshot, err := stream.Snapshot(ctx)
		var failure *transport.RequestError
		if err == nil || !errors.As(err, &failure) || failure.Status != 503 || failure.Code != "state_unavailable" {
			return snapshot, err
		}
		select {
		case <-ctx.Done():
			return contract.Snapshot{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
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
	pending := make([]uuid.UUID, 0, len(s.commands))
	for id, command := range s.commands {
		if !terminalCommand(command.Status) && !s.inFlight[id] && !s.ambiguous[id] {
			pending = append(pending, id)
		}
	}
	s.mu.Unlock()
	for _, id := range pending {
		requestCtx, cancel := context.WithTimeout(ctx, sessionRequestLimit)
		command, err := s.client.Command(requestCtx, lease, id)
		cancel()
		if err == nil {
			s.mu.Lock()
			if existing, ok := s.commands[id]; ok && !s.inFlight[id] && !s.ambiguous[id] {
				s.commands[id] = mergeCommandStatus(existing, command)
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

func (s *demoSession) currentLease() (uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.leaseConfirmed || s.leaseID == uuid.Nil || !time.Now().Before(s.leaseUntil) {
		return uuid.Nil, errSessionUnavailable
	}
	return s.leaseID, nil
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

func (s *demoSession) commandAllowedLocked(stop bool) bool {
	now := time.Now()
	valid := !s.closed && !s.closing && s.leaseID != uuid.Nil
	if stop {
		return valid
	}
	for id, command := range s.commands {
		if strings.HasPrefix(s.commandPayloads[id], "stop:") && !terminalCommand(command.Status) && command.TestId != nil && *command.TestId == s.activeTestID && s.activeTestID != uuid.Nil {
			return false
		}
	}
	return s.liveLocked(now)
}

func (s *demoSession) liveLocked(now time.Time) bool {
	valid := !s.closed && !s.closing && s.leaseID != uuid.Nil
	valid = valid && s.leaseConfirmed && now.Before(s.leaseUntil) && !s.streamBroken && !s.unknownAcquire && s.streamActive && s.streamDone != nil && s.stream != nil && !s.stream.Pending() && !s.snapshotPending && s.snapshot != nil && !s.lastUI.IsZero() && now.Sub(s.lastUI) < uiHeartbeatFreshness && !s.lastProbe.IsZero() && now.Sub(s.lastProbe) < streamProbeFreshness
	if valid {
		select {
		case <-s.streamDone:
			valid = false
		default:
		}
	}
	return valid
}

func (s *demoSession) invalidateActionsLocked() {
	s.cancelActions()
	if !s.closed && !s.closing {
		s.actionsCtx, s.cancelActions = context.WithCancel(s.ctx)
	}
}

func (s *demoSession) startLiveLocked(now time.Time) {
	if s.liveMark.IsZero() && s.liveLocked(now) {
		s.liveMark = now
	}
}

func (s *demoSession) pauseLiveLocked(now time.Time) {
	if s.liveMark.IsZero() {
		return
	}
	end := now
	for _, limit := range []time.Time{s.lastUI.Add(uiHeartbeatFreshness), s.lastProbe.Add(streamProbeFreshness), s.leaseUntil} {
		if !limit.IsZero() && limit.Before(end) {
			end = limit
		}
	}
	if s.stream != nil {
		if pending := s.stream.PendingSince(); !pending.IsZero() && pending.Before(end) {
			end = pending
		}
	}
	if end.After(s.liveMark) {
		s.liveDuration += end.Sub(s.liveMark)
	}
	s.liveMark = time.Time{}
	if s.liveDuration >= streamProbeFreshness {
		s.recoverySince = time.Time{}
		s.liveDuration = 0
	}
}

// command registers the identifier before sending and never resends it automatically.
func (s *demoSession) command(ctx context.Context, id uuid.UUID, identity string, stop bool, send func(context.Context, uuid.UUID) (contract.CommandStatus, error), invalidNew ...error) (contract.CommandStatus, error) {
	if id == uuid.Nil {
		return contract.CommandStatus{}, errSessionUnavailable
	}
	s.mu.Lock()
	if !stop {
		if _, reserved := s.knownStops[id]; reserved {
			s.mu.Unlock()
			return contract.CommandStatus{}, errors.New("command identifier is reserved for stopping")
		}
	}
	if existing, ok := s.commands[id]; ok {
		if s.commandPayloads[id] != identity {
			if !stop {
				s.mu.Unlock()
				return contract.CommandStatus{}, errors.New("command identifier already used with different content")
			}
			if !s.commandAllowedLocked(true) {
				s.mu.Unlock()
				return contract.CommandStatus{}, errSessionUnavailable
			}
			delete(s.commands, id)
			delete(s.commandPayloads, id)
			delete(s.inFlight, id)
			delete(s.ambiguous, id)
		} else {
			if stop && existing.Status == contract.CommandStatusStatusUnknown && !s.inFlight[id] {
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
			if terminalCommand(status.Status) && !s.inFlight[previous] && s.createCoveredLocked(previous, status, s.snapshot) {
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
		admitted := s.commandPayloads[id] == identity && s.leaseID == lease && s.commandAllowedLocked(false) && requestCtx.Err() == nil
		if admitted {
			admissionDeadline = s.lastUI.Add(uiHeartbeatFreshness)
			if deadline := s.lastProbe.Add(streamProbeFreshness); deadline.Before(admissionDeadline) {
				admissionDeadline = deadline
			}
			if s.leaseUntil.Before(admissionDeadline) {
				admissionDeadline = s.leaseUntil
			}
			streamDone = s.streamDone
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
			delete(s.commands, id)
			delete(s.commandPayloads, id)
			delete(s.ambiguous, id)
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
	unconfirmed := unknownAcquire || leasePending || s.cleanupUnknown
	if active != nil && (active.Stage != "finished" || active.SaveStatus != "saved") {
		unconfirmed = true
	}
	if s.activeTestID != uuid.Nil && (active == nil || active.TestId != s.activeTestID) {
		unconfirmed = true
	}
	for id, status := range s.commands {
		if !terminalCommand(status.Status) || !s.createCoveredLocked(id, status, s.snapshot) || (active != nil && active.Stage == "finished" && id == active.StopCommandId && status.Status != contract.CommandStatusStatusCompleted) {
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
		} else if active != nil && active.Stage == "finished" && id == active.StopCommandId && status.Status != contract.CommandStatusStatusCompleted {
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
		status, err := s.client.Command(ctx, lease, id)
		s.mu.Lock()
		if existing, ok := s.commands[id]; ok && !s.inFlight[id] && !s.ambiguous[id] {
			if err == nil {
				s.commands[id] = mergeCommandStatus(existing, status)
			}
			if terminalCommand(s.commands[id].Status) {
				status, err = s.commands[id], nil
			}
		}
		s.mu.Unlock()
		stopForActive := active != nil && id == active.StopCommandId
		if stopForActive && active.Stage == "finished" {
			if err != nil || status.Status != contract.CommandStatusStatusCompleted {
				unknown = true
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
	cleanupUnknown := s.cleanupUnknown
	s.mu.Unlock()
	if unknown || !snapshotKnown || cleanupUnknown {
		result = ErrSessionShutdownUnconfirmed
	}
	if active != nil && (active.Stage != "finished" || confirmFinishedStop) {
		stopID := active.StopCommandId
		if active.Stage != "finished" {
			s.startShutdownStop(ctx, lease, active)
		}
		for {
			s.mu.Lock()
			cached, tracked := s.commands[stopID]
			s.mu.Unlock()
			if tracked && cached.Status == contract.CommandStatusStatusCompleted {
				break
			}
			status, err := s.client.Command(ctx, lease, stopID)
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
		}
	}
	s.mu.Unlock()
}

func (s *demoSession) wait(ctx context.Context) error {
	s.mu.Lock()
	stopped := s.streamStopped
	s.mu.Unlock()
	if stopped == nil {
		return nil
	}
	select {
	case <-stopped:
		s.mu.Lock()
		unknown := s.cleanupUnknown
		s.mu.Unlock()
		if unknown {
			return ErrSessionShutdownUnconfirmed
		}
		return nil
	case <-ctx.Done():
		return ErrSessionShutdownUnconfirmed
	}
}
