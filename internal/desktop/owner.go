package desktop

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/diagnostics"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

type ownedCore interface {
	Handshake(context.Context) (contract.Handshake, error)
	Done() <-chan struct{}
	DiagnosticsDegraded() <-chan struct{}
	ExitCode() (int, bool)
	Close(context.Context) error
}

type startCore func(context.Context) (ownedCore, contract.Handshake, error)

var errOwnerShutdownUnconfirmed = errors.New("desktop owner shutdown could not be confirmed")

// Owner serializes startup, observation and shutdown of its own core process.
type Owner struct {
	mu                sync.Mutex
	status            contract.DesktopStatus
	process           ownedCore
	session           *demoSession
	cleanupErr        error
	sessionErr        error
	coreExitLogged    bool
	backendID         uuid.UUID
	verifyGate        chan struct{}
	starter           startCore
	log               *slog.Logger
	notify            func(contract.DesktopStatus)
	notifications     chan struct{}
	connectionChanges chan struct{}
	connectionMu      sync.Mutex
	connectionEmitter func(contract.ConnectionState)
	connectionActive  bool
	connectionPending *contract.ConnectionState
	connectionDirty   bool
	eventChanges      chan struct{}
	eventMu           sync.Mutex
	eventEmitter      func(contract.Event)
	eventPending      *contract.Event
	eventLatest       *contract.Event
	watchMu           sync.Mutex
	watchChanges      chan struct{}
	stopNotifications chan struct{}
	hostDegraded      <-chan struct{}
	ctx               context.Context
	cancel            context.CancelFunc
	workerDone        chan struct{}
	startOnce         sync.Once
	closeOnce         sync.Once
	notifyStopOnce    sync.Once
}

// NewOwner uses the production native-process transport.
func NewOwner(hostID uuid.UUID, executable, profileDir, profileID, version string, log *slog.Logger, hostDegraded <-chan struct{}, degraded bool, notify func(contract.DesktopStatus)) *Owner {
	starter := func(ctx context.Context) (ownedCore, contract.Handshake, error) {
		process, handshake, err := transport.Start(ctx, executable, profileDir, profileID, version)
		if process == nil {
			return nil, handshake, err
		}
		return process, handshake, err
	}
	owner := newOwner(hostID, starter, log, degraded, notify)
	owner.hostDegraded = hostDegraded
	return owner
}

func newOwner(hostID uuid.UUID, starter startCore, log *slog.Logger, degraded bool, notify func(contract.DesktopStatus)) *Owner {
	ctx, cancel := context.WithCancel(context.Background())
	return &Owner{
		status:  contract.DesktopStatus{HostRunId: hostID, Revision: 1, Status: contract.DesktopStatusStatusStarting, DiagnosticsDegraded: degraded},
		starter: starter, log: log, notify: notify, notifications: make(chan struct{}, 1), connectionChanges: make(chan struct{}, 1), eventChanges: make(chan struct{}, 1), watchChanges: make(chan struct{}), stopNotifications: make(chan struct{}),
		ctx: ctx, cancel: cancel, workerDone: make(chan struct{}),
		verifyGate: make(chan struct{}, 1),
	}
}

func (o *Owner) Start() {
	o.startOnce.Do(func() {
		go o.notifyLoop()
		go o.connectionLoop()
		go o.eventLoop()
		go o.run()
		if o.hostDegraded != nil {
			go o.watchHostDiagnostics()
		}
	})
}

func (o *Owner) SetCoreEventEmitter(emit func(contract.Event)) {
	o.eventMu.Lock()
	o.eventEmitter = emit
	o.eventMu.Unlock()
}

func (o *Owner) observeCoreEvent(event contract.Event) {
	if event.Sequence <= 0 {
		return
	}
	o.eventMu.Lock()
	copy := event
	o.eventLatest = &copy
	o.eventMu.Unlock()
	select {
	case o.eventChanges <- struct{}{}:
	default:
	}
}

func (o *Owner) eventLoop() {
	for {
		select {
		case <-o.ctx.Done():
			return
		case <-o.eventChanges:
			o.connectionMu.Lock()
			active := o.connectionActive
			o.connectionMu.Unlock()
			o.eventMu.Lock()
			if !active || o.eventEmitter == nil || o.eventLatest == nil || o.eventPending != nil {
				o.eventMu.Unlock()
				continue
			}
			copy := *o.eventLatest
			o.eventPending = &copy
			emit := o.eventEmitter
			o.eventMu.Unlock()
			emit(copy)
		}
	}
}

func (o *Owner) AcknowledgeCoreEvent(_ context.Context, backend string, sequence int64) error {
	o.eventMu.Lock()
	if o.eventPending == nil || o.eventPending.BackendRunId.String() != backend || o.eventPending.Sequence != sequence {
		o.eventMu.Unlock()
		return errSessionUnavailable
	}
	o.eventPending = nil
	more := o.eventLatest != nil && o.eventLatest.Sequence > sequence
	o.eventMu.Unlock()
	if more {
		select {
		case o.eventChanges <- struct{}{}:
		default:
		}
	}
	return nil
}

func (o *Owner) connectionNotice(view contract.SessionView, afterSequence int64) contract.ConnectionNotice {
	result := contract.ConnectionNotice{Connection: view.Connection}
	if view.Snapshot != nil {
		result.SnapshotBoundary = view.Snapshot.LastEventSequence
	}
	o.eventMu.Lock()
	defer o.eventMu.Unlock()
	if o.eventLatest != nil && o.eventLatest.Sequence > afterSequence {
		copy := *o.eventLatest
		result.Event = &copy
		result.Gap = copy.Sequence-afterSequence > 1
	}
	if result.SnapshotBoundary > afterSequence && (result.Event == nil || result.SnapshotBoundary > result.Event.Sequence) {
		result.Gap = true
	}
	return result
}

// SetConnectionEmitter installs the native UI notification sink before Start.
func (o *Owner) SetConnectionEmitter(emit func(contract.ConnectionState)) {
	o.connectionMu.Lock()
	o.connectionEmitter = emit
	o.connectionMu.Unlock()
}

func (o *Owner) signalConnection() {
	o.watchMu.Lock()
	if o.watchChanges != nil {
		close(o.watchChanges)
	}
	o.watchChanges = make(chan struct{})
	o.watchMu.Unlock()
	select {
	case o.connectionChanges <- struct{}{}:
	default:
	}
}

// WaitConnection is a bounded fallback when an emitted event was lost during reload.
func (o *Owner) WaitConnection(ctx context.Context, after, afterSequence int64) (contract.ConnectionNotice, error) {
	if after < 0 || afterSequence < 0 {
		return contract.ConnectionNotice{}, errSessionUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		o.watchMu.Lock()
		changes := o.watchChanges
		o.watchMu.Unlock()
		view, err := o.SessionView(ctx)
		if err != nil {
			return contract.ConnectionNotice{}, err
		}
		state := view.Connection
		if state.Revision > after {
			return o.connectionNotice(view, afterSequence), nil
		}
		select {
		case <-changes:
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				view, err := o.SessionView(context.Background())
				if err != nil {
					return contract.ConnectionNotice{}, err
				}
				return o.connectionNotice(view, afterSequence), nil
			}
			return contract.ConnectionNotice{}, ctx.Err()
		case <-o.ctx.Done():
			return contract.ConnectionNotice{}, errSessionUnavailable
		}
	}
}

func (o *Owner) connectionLoop() {
	for {
		select {
		case <-o.ctx.Done():
			return
		case <-o.connectionChanges:
			o.mu.Lock()
			session := o.session
			o.mu.Unlock()
			if session == nil {
				continue
			}
			state := session.connectionState()
			o.connectionMu.Lock()
			if !o.connectionActive || o.connectionEmitter == nil {
				o.connectionMu.Unlock()
				continue
			}
			if o.connectionPending != nil {
				o.connectionDirty = true
				o.connectionMu.Unlock()
				continue
			}
			copy := state
			o.connectionPending = &copy
			emit := o.connectionEmitter
			o.connectionMu.Unlock()
			emit(state)
		}
	}
}

func (o *Owner) EnableConnectionEvents(context.Context) error {
	o.connectionMu.Lock()
	o.connectionActive = true
	o.connectionMu.Unlock()
	o.signalConnection()
	select {
	case o.eventChanges <- struct{}{}:
	default:
	}
	return nil
}

func (o *Owner) AcknowledgeConnection(_ context.Context, host string, revision int64) error {
	o.connectionMu.Lock()
	pending := o.connectionPending
	if pending == nil || pending.HostRunId.String() != host || pending.Revision != revision {
		o.connectionMu.Unlock()
		return errSessionUnavailable
	}
	o.connectionPending = nil
	dirty := o.connectionDirty
	o.connectionDirty = false
	o.connectionMu.Unlock()
	if dirty {
		o.signalConnection()
	}
	return nil
}

func (o *Owner) notifyLoop() {
	for {
		select {
		case <-o.notifications:
			o.notify(o.Status())
		case <-o.stopNotifications:
			return
		}
	}
}

func (o *Owner) signal() {
	select {
	case o.notifications <- struct{}{}:
	default:
	}
}

func (o *Owner) watchHostDiagnostics() {
	select {
	case <-o.hostDegraded:
		o.markDegraded()
	case <-o.ctx.Done():
	}
}

func (o *Owner) markDegraded() {
	o.mu.Lock()
	if o.status.DiagnosticsDegraded {
		o.mu.Unlock()
		return
	}
	o.status.DiagnosticsDegraded = true
	o.status.Revision++
	closing := o.status.Status == contract.DesktopStatusStatusClosing
	o.mu.Unlock()
	o.log.Warn(string(diagnostics.DiagnosticDegraded))
	if !closing {
		o.signal()
	}
}

func (o *Owner) run() {
	defer close(o.workerDone)
	process, handshake, err := o.starter(o.ctx)
	o.mu.Lock()
	o.process = process
	if err == nil {
		o.backendID = handshake.BackendRunId
	}
	closing := o.status.Status == contract.DesktopStatusStatusClosing
	o.mu.Unlock()
	if closing {
		return
	}
	if process != nil {
		defer func() {
			closeErr := process.Close(context.Background())
			select {
			case <-process.DiagnosticsDegraded():
				o.markDegraded()
			default:
			}
			if closeErr != nil {
				o.mu.Lock()
				o.cleanupErr = closeErr
				o.mu.Unlock()
			}
			o.recordCoreExit(process)
		}()
	}
	if err != nil {
		status := contract.DesktopStatusStatusUnavailable
		if errors.Is(err, transport.ErrProfileInUse) {
			status = contract.DesktopStatusStatusProfileInUse
		}
		o.setState(status, nil)
		o.log.Error(string(diagnostics.ConnectionFailed), "error_code", "unavailable")
		return
	}
	if process == nil {
		o.setState(contract.DesktopStatusStatusUnavailable, nil)
		return
	}
	if native, ok := process.(interface{ Client() *transport.Client }); ok && native.Client() != nil {
		o.mu.Lock()
		session := newDemoSession(o.ctx, nativeSessionClient{native.Client()}, o.status.HostRunId, handshake.BackendRunId)
		session.onChange = o.signalConnection
		session.onEvent = o.observeCoreEvent
		session.onState = func(previous, current bool) {
			label := func(valid bool) string {
				if valid {
					return "connected"
				}
				return "disconnected"
			}
			o.log.Info(string(diagnostics.ConnectionStateChanged), "previous_status", label(previous), "connection_status", label(current), "backend_run_id", handshake.BackendRunId)
		}
		o.session = session
		o.mu.Unlock()
		defer func() {
			session.close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := session.wait(ctx); err != nil {
				o.mu.Lock()
				o.sessionErr = err
				o.mu.Unlock()
			}
		}()
	}
	o.setState(contract.DesktopStatusStatusReady, &handshake)
	o.log.Info(string(diagnostics.ConnectionConfirmed), "backend_run_id", handshake.BackendRunId)
	degraded := process.DiagnosticsDegraded()
	for {
		select {
		case <-o.ctx.Done():
			return
		case <-degraded:
			o.markDegraded()
			degraded = nil
		case <-process.Done():
			select {
			case <-process.DiagnosticsDegraded():
				o.markDegraded()
			default:
			}
			o.markExited(process)
			return
		}
	}
}

func (o *Owner) recordCoreExit(process ownedCore) {
	code, ok := process.ExitCode()
	if !ok {
		return
	}
	o.mu.Lock()
	if o.coreExitLogged {
		o.mu.Unlock()
		return
	}
	o.coreExitLogged = true
	backendID := o.backendID
	o.mu.Unlock()
	o.log.Info(string(diagnostics.CoreStopped), "exit_code", code, "backend_run_id", backendID)
}

func (o *Owner) setState(status contract.DesktopStatusStatus, handshake *contract.Handshake) {
	o.mu.Lock()
	if o.status.Status == contract.DesktopStatusStatusClosing {
		o.mu.Unlock()
		return
	}
	o.status.Status, o.status.Handshake = status, handshake
	o.status.Revision++
	o.status.ConnectionRevision++
	o.mu.Unlock()
	o.signal()
}

func (o *Owner) markExited(process ownedCore) {
	o.mu.Lock()
	if o.process != process || o.status.Status == contract.DesktopStatusStatusClosing || o.status.Status == contract.DesktopStatusStatusExited {
		o.mu.Unlock()
		return
	}
	o.status.Status, o.status.Handshake = contract.DesktopStatusStatusExited, nil
	o.status.Revision++
	o.status.ConnectionRevision++
	o.mu.Unlock()
	o.signal()
	if code, ok := process.ExitCode(); ok {
		o.log.Error(string(diagnostics.ConnectionFailed), "exit_code", code)
	}
}

// Status is a snapshot; callers must order it against events by hostRunId and revision.
func (o *Owner) Status() contract.DesktopStatus {
	o.mu.Lock()
	defer o.mu.Unlock()
	result := o.status
	if result.Handshake != nil {
		copy := *result.Handshake
		result.Handshake = &copy
	}
	return result
}

// CoreExitConfirmed reports only a signalled exit of the owned core process.
func (o *Owner) CoreExitConfirmed() bool {
	o.mu.Lock()
	process := o.process
	o.mu.Unlock()
	if process == nil {
		return false
	}
	select {
	case <-process.Done():
		return true
	default:
		return false
	}
}

// Verify never lets an old reply overwrite a later exit or shutdown.
func (o *Owner) Verify(ctx context.Context) contract.DesktopStatus {
	select {
	case o.verifyGate <- struct{}{}:
		defer func() { <-o.verifyGate }()
	case <-ctx.Done():
		return o.Status()
	}
	o.mu.Lock()
	process := o.process
	state := o.status.Status
	backendID := o.backendID
	o.mu.Unlock()
	if (state != contract.DesktopStatusStatusReady && state != contract.DesktopStatusStatusUnavailable) || process == nil || backendID == uuid.Nil {
		return o.Status()
	}
	handshake, err := process.Handshake(ctx)
	if err == nil && (handshake.BackendRunId != backendID || handshake.ApiVersion != "v1" || !handshake.Demo) {
		err = errSessionUnavailable
	}
	if ctx.Err() != nil {
		return o.Status()
	}
	o.mu.Lock()
	if o.process != process || o.status.Status != state {
		o.mu.Unlock()
		return o.Status()
	}
	select {
	case <-process.Done():
		o.mu.Unlock()
		o.markExited(process)
		return o.Status()
	default:
		if err != nil {
			o.status.Status, o.status.Handshake = contract.DesktopStatusStatusUnavailable, nil
		} else {
			o.status.Status = contract.DesktopStatusStatusReady
			o.status.Handshake = &handshake
		}
	}
	o.status.Revision++
	o.status.ConnectionRevision++
	o.mu.Unlock()
	if err != nil {
		o.log.Error(string(diagnostics.ConnectionFailed), "error_code", "unavailable", "backend_run_id", backendID)
	}
	o.signal()
	return o.Status()
}

// Close waits for startup and closes only the owned core using a fresh context.
func (o *Owner) Close() error {
	o.closeOnce.Do(func() {
		o.mu.Lock()
		o.status.Status, o.status.Handshake = contract.DesktopStatusStatusClosing, nil
		o.status.Revision++
		o.status.ConnectionRevision++
		session := o.session
		o.mu.Unlock()
		if session != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			err := session.shutdown(ctx)
			cancel()
			if err != nil {
				o.mu.Lock()
				o.sessionErr = err
				o.mu.Unlock()
			}
		}
		o.cancel()
		o.startOnce.Do(func() { close(o.workerDone) })
	})
	select {
	case <-o.workerDone:
	case <-time.After(10 * time.Second):
		o.mu.Lock()
		process := o.process
		sessionErr := o.sessionErr
		o.mu.Unlock()
		if process != nil {
			select {
			case <-process.Done():
				return errors.Join(errOwnerShutdownUnconfirmed, sessionErr)
			default:
			}
		}
		return errors.Join(errOwnerShutdownUnconfirmed, transport.ErrCleanupUnconfirmed, sessionErr)
	}
	o.notifyStopOnce.Do(func() { close(o.stopNotifications) })
	select {
	case <-o.hostDegraded:
		o.markDegraded()
	default:
	}
	o.mu.Lock()
	process := o.process
	cleanupErr := o.cleanupErr
	sessionErr := o.sessionErr
	o.mu.Unlock()
	if process != nil {
		err := process.Close(context.Background())
		o.recordCoreExit(process)
		select {
		case <-process.DiagnosticsDegraded():
			o.markDegraded()
		default:
		}
		if err != nil {
			cleanupErr = err
		}
		if cleanupErr != nil {
			select {
			case <-process.Done():
				return errors.Join(errors.New("core stopped with an error"), sessionErr)
			default:
				return errors.Join(transport.ErrCleanupUnconfirmed, sessionErr)
			}
		}
	}
	return sessionErr
}
