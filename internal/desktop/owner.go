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
	delivery       *observationDelivery
	mu             sync.Mutex
	status         contract.DesktopStatus
	process        ownedCore
	session        *demoSession
	cleanupErr     error
	sessionErr     error
	coreExitLogged bool
	backendID      uuid.UUID
	databaseID     uuid.UUID
	verifyGate     chan struct{}
	starter        startCore
	log            *slog.Logger
	hostDegraded   <-chan struct{}
	ctx            context.Context
	cancel         context.CancelFunc
	workerDone     chan struct{}
	startOnce      sync.Once
	closeOnce      sync.Once
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
	owner := &Owner{
		status:  contract.DesktopStatus{HostRunId: hostID, Revision: 1, Status: contract.DesktopStatusStatusStarting, DiagnosticsDegraded: degraded},
		starter: starter, log: log,
		ctx: ctx, cancel: cancel, workerDone: make(chan struct{}),
		verifyGate: make(chan struct{}, 1),
	}
	owner.delivery = newObservationDelivery(ctx, owner.Status, owner.deliveryConnection, owner.SessionView, notify)
	return owner
}

func (o *Owner) Start() {
	o.startOnce.Do(func() {
		o.delivery.start()
		go o.run()
		if o.hostDegraded != nil {
			go o.watchHostDiagnostics()
		}
	})
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
	if err == nil && (handshake.DatabaseId == uuid.Nil || handshake.BackendRunId == uuid.Nil || handshake.ApiVersion != "v1" || !handshake.Demo) {
		err = errSessionUnavailable
	}
	o.mu.Lock()
	o.process = process
	if err == nil {
		o.backendID = handshake.BackendRunId
		o.databaseID = handshake.DatabaseId
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
		session.databaseID = handshake.DatabaseId
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
	databaseID := o.databaseID
	o.mu.Unlock()
	if (state != contract.DesktopStatusStatusReady && state != contract.DesktopStatusStatusUnavailable) || process == nil || backendID == uuid.Nil || databaseID == uuid.Nil {
		return o.Status()
	}
	handshake, err := process.Handshake(ctx)
	if err == nil && (handshake.BackendRunId != backendID || handshake.DatabaseId != databaseID || handshake.ApiVersion != "v1" || !handshake.Demo) {
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
	o.delivery.stopStatusNotifications()
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

// deliveryConnection preserves the raw session observation used by native push.
// Unlike the public read path, it does not require bootstrap ready.
func (o *Owner) deliveryConnection() (contract.ConnectionState, bool) {
	o.mu.Lock()
	session := o.session
	o.mu.Unlock()
	if session == nil {
		return contract.ConnectionState{}, false
	}
	return session.connectionState(), true
}
func (o *Owner) SetCoreEventEmitter(emit func(contract.Event)) { o.delivery.setCoreEventEmitter(emit) }
func (o *Owner) SetConnectionEmitter(emit func(contract.ConnectionState)) {
	o.delivery.setConnectionEmitter(emit)
}
func (o *Owner) AcknowledgeCoreEvent(_ context.Context, backend string, sequence int64) error {
	return o.delivery.acknowledgeCoreEvent(backend, sequence)
}
func (o *Owner) AcknowledgeConnection(_ context.Context, host string, revision int64) error {
	return o.delivery.acknowledgeConnection(host, revision)
}
func (o *Owner) EnableConnectionEvents(context.Context) error {
	return o.delivery.enableConnectionEvents()
}
func (o *Owner) WaitConnection(ctx context.Context, after, afterSequence int64) (contract.ConnectionNotice, error) {
	return o.delivery.waitConnection(ctx, after, afterSequence)
}
func (o *Owner) signalConnection()                     { o.delivery.signalConnection() }
func (o *Owner) observeCoreEvent(event contract.Event) { o.delivery.observeCoreEvent(event) }
func (o *Owner) signal()                               { o.delivery.signalStatus() }
