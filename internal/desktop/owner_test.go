package desktop

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

type fakeCore struct {
	done      chan struct{}
	degraded  chan struct{}
	closed    chan struct{}
	once      sync.Once
	handshake func(context.Context) (contract.Handshake, error)
	closeErr  error
	onClose   func()
}

func (f *fakeCore) Done() <-chan struct{}                { return f.done }
func (f *fakeCore) DiagnosticsDegraded() <-chan struct{} { return f.degraded }
func (f *fakeCore) ExitCode() (int, bool)                { return 7, true }
func (f *fakeCore) Close(context.Context) error {
	f.once.Do(func() {
		if f.onClose != nil {
			f.onClose()
		}
		close(f.closed)
	})
	return f.closeErr
}
func (f *fakeCore) Handshake(ctx context.Context) (contract.Handshake, error) {
	return f.handshake(ctx)
}

func waitStatus(t *testing.T, owner *Owner, expected contract.DesktopStatusStatus) contract.DesktopStatus {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		current := owner.Status()
		if current.Status == expected {
			return current
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatalf("status %s not reached: %+v", expected, current)
		}
	}
}

func newTestOwner(starter startCore) *Owner {
	return newOwner(uuid.New(), starter, slog.New(slog.NewTextHandler(io.Discard, nil)), false, func(contract.DesktopStatus) {})
}

func TestConnectionNotificationsWaitForDeliveryAcknowledgement(t *testing.T) {
	owner := newTestOwner(nil)
	session := newDemoSession(owner.ctx, &testSessionClient{}, uuid.New(), uuid.New())
	defer session.close()
	defer owner.cancel()
	session.onChange = owner.signalConnection
	owner.session = session
	events := make(chan contract.ConnectionState, 4)
	owner.SetConnectionEmitter(func(state contract.ConnectionState) { events <- state })
	go owner.connectionLoop()
	if err := owner.EnableConnectionEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := <-events
	session.mu.Lock()
	for range 20 {
		session.changedLocked()
	}
	session.mu.Unlock()
	select {
	case event := <-events:
		t.Fatalf("unacknowledged notification was overwritten: %+v", event)
	case <-time.After(50 * time.Millisecond):
	}
	if err := owner.AcknowledgeConnection(context.Background(), first.HostRunId.String(), first.Revision+1); err == nil {
		t.Fatal("wrong revision acknowledged")
	}
	if err := owner.AcknowledgeConnection(context.Background(), first.HostRunId.String(), first.Revision); err != nil {
		t.Fatal(err)
	}
	select {
	case next := <-events:
		if next.Revision <= first.Revision {
			t.Fatalf("change was lost: %+v", next)
		}
	case <-time.After(time.Second):
		t.Fatal("pending change was not sent after acknowledgement")
	}
}

func TestWaitConnectionObservesChangesWithoutEventAcknowledgement(t *testing.T) {
	owner := newTestOwner(nil)
	defer owner.cancel()
	session := newDemoSession(owner.ctx, &testSessionClient{}, uuid.New(), uuid.New())
	defer session.close()
	session.onChange = owner.signalConnection
	owner.session = session
	owner.process = &fakeCore{done: make(chan struct{})}
	owner.status.Status = contract.DesktopStatusStatusReady
	first := session.connectionState()
	done := make(chan contract.ConnectionNotice, 1)
	errs := make(chan error, 1)
	go func() {
		state, err := owner.WaitConnection(context.Background(), first.Revision, 0)
		done <- state
		errs <- err
	}()
	session.mu.Lock()
	session.changedLocked()
	session.mu.Unlock()
	select {
	case state := <-done:
		if err := <-errs; err != nil || state.Connection.Revision <= first.Revision {
			t.Fatalf("missed connection change: %+v %v", state, err)
		}
	case <-time.After(time.Second):
		t.Fatal("connection change was not observed")
	}
}

func TestCoreEventDeliveryIsBoundedAndReportsGap(t *testing.T) {
	owner := newTestOwner(nil)
	defer owner.cancel()
	events := make(chan contract.Event, 3)
	owner.SetCoreEventEmitter(func(event contract.Event) { events <- event })
	go owner.eventLoop()
	if err := owner.EnableConnectionEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend := uuid.New()
	owner.observeCoreEvent(contract.Event{BackendRunId: backend, Sequence: 1, Type: contract.EventTypeStateChanged})
	first := <-events
	owner.observeCoreEvent(contract.Event{BackendRunId: backend, Sequence: 2, Type: contract.EventTypeStateChanged})
	owner.observeCoreEvent(contract.Event{BackendRunId: backend, Sequence: 3, Type: contract.EventTypeStateChanged})
	select {
	case event := <-events:
		t.Fatalf("unacknowledged event overwritten: %+v", event)
	case <-time.After(50 * time.Millisecond):
	}
	if err := owner.AcknowledgeCoreEvent(context.Background(), backend.String(), first.Sequence); err != nil {
		t.Fatal(err)
	}
	select {
	case next := <-events:
		if next.Sequence != 3 {
			t.Fatalf("unexpected newest event: %+v", next)
		}
	case <-time.After(time.Second):
		t.Fatal("event not sent after acknowledgement")
	}
	notice := owner.connectionNotice(contract.SessionView{}, 1)
	if !notice.Gap || notice.Event == nil || notice.Event.Sequence != 3 {
		t.Fatalf("gap not exposed: %+v", notice)
	}
	covered := owner.connectionNotice(contract.SessionView{Snapshot: &contract.Snapshot{LastEventSequence: 4}}, 3)
	if !covered.Gap || covered.SnapshotBoundary != 4 {
		t.Fatalf("snapshot-covered event loss not exposed: %+v", covered)
	}
}

func TestOwnerReportsDegradedAndCoreExit(t *testing.T) {
	identity := contract.Handshake{ApiVersion: "v1", BackendRunId: uuid.New(), Demo: true}
	core := &fakeCore{done: make(chan struct{}), degraded: make(chan struct{}), closed: make(chan struct{}), handshake: func(context.Context) (contract.Handshake, error) { return identity, nil }}
	owner := newTestOwner(func(context.Context) (ownedCore, contract.Handshake, error) { return core, identity, nil })
	owner.Start()
	ready := waitStatus(t, owner, contract.DesktopStatusStatusReady)
	if ready.Handshake == nil || ready.Handshake.BackendRunId != identity.BackendRunId {
		t.Fatal("missing confirmed identity")
	}
	close(core.degraded)
	deadline := time.After(time.Second)
	for !owner.Status().DiagnosticsDegraded {
		select {
		case <-deadline:
			t.Fatal("diagnostics not marked degraded")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	close(core.done)
	exited := waitStatus(t, owner, contract.DesktopStatusStatusExited)
	if exited.Handshake != nil || exited.Revision <= ready.Revision {
		t.Fatal("stale handshake survived core exit")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerClosesProcessReturnedAfterStartupCancellation(t *testing.T) {
	core := &fakeCore{done: make(chan struct{}), degraded: make(chan struct{}), closed: make(chan struct{})}
	started := make(chan struct{})
	owner := newTestOwner(func(ctx context.Context) (ownedCore, contract.Handshake, error) {
		close(started)
		<-ctx.Done()
		return core, contract.Handshake{}, transport.ErrCleanupUnconfirmed
	})
	owner.Start()
	<-started
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-core.closed:
	case <-time.After(time.Second):
		t.Fatal("late process was not closed")
	}
	if owner.Status().Status != contract.DesktopStatusStatusClosing {
		t.Fatal("closed owner returned to an active state")
	}
}

func TestOwnerDistinguishesOccupiedProfile(t *testing.T) {
	owner := newTestOwner(func(context.Context) (ownedCore, contract.Handshake, error) {
		return nil, contract.Handshake{}, transport.ErrProfileInUse
	})
	owner.Start()
	waitStatus(t, owner, contract.DesktopStatusStatusProfileInUse)
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDelayedVerificationCannotRestoreExitedCore(t *testing.T) {
	identity := contract.Handshake{ApiVersion: "v1", BackendRunId: uuid.New(), Demo: true}
	started, release := make(chan struct{}), make(chan struct{})
	core := &fakeCore{done: make(chan struct{}), degraded: make(chan struct{}), closed: make(chan struct{}), handshake: func(context.Context) (contract.Handshake, error) { close(started); <-release; return identity, nil }}
	owner := newTestOwner(func(context.Context) (ownedCore, contract.Handshake, error) { return core, identity, nil })
	owner.Start()
	waitStatus(t, owner, contract.DesktopStatusStatusReady)
	verification := make(chan contract.DesktopStatus, 1)
	go func() { verification <- owner.Verify(context.Background()) }()
	<-started
	close(core.done)
	waitStatus(t, owner, contract.DesktopStatusStatusExited)
	close(release)
	if result := <-verification; result.Status != contract.DesktopStatusStatusExited || result.Handshake != nil {
		t.Fatal("late reply restored stale handshake")
	}
	if err := owner.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestOwnerRecoversVerifiedConnectionToSameCore(t *testing.T) {
	identity := contract.Handshake{ApiVersion: "v1", BackendRunId: uuid.New(), Demo: true}
	var calls int
	core := &fakeCore{done: make(chan struct{}), degraded: make(chan struct{}), closed: make(chan struct{}), handshake: func(context.Context) (contract.Handshake, error) {
		calls++
		if calls == 1 {
			return contract.Handshake{}, errors.New("connection interrupted")
		}
		return identity, nil
	}}
	owner := newTestOwner(func(context.Context) (ownedCore, contract.Handshake, error) { return core, identity, nil })
	owner.Start()
	waitStatus(t, owner, contract.DesktopStatusStatusReady)
	if status := owner.Verify(context.Background()); status.Status != contract.DesktopStatusStatusUnavailable {
		t.Fatalf("failed verification: %+v", status)
	}
	if status := owner.Verify(context.Background()); status.Status != contract.DesktopStatusStatusReady || status.Handshake == nil || status.Handshake.BackendRunId != identity.BackendRunId {
		t.Fatalf("same core was not restored: %+v", status)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerPreservesUnconfirmedSessionShutdown(t *testing.T) {
	identity := contract.Handshake{ApiVersion: "v1", BackendRunId: uuid.New(), Demo: true}
	core := &fakeCore{done: make(chan struct{}), degraded: make(chan struct{}), closed: make(chan struct{}), handshake: func(context.Context) (contract.Handshake, error) { return identity, nil }}
	owner := newTestOwner(func(context.Context) (ownedCore, contract.Handshake, error) { return core, identity, nil })
	owner.Start()
	waitStatus(t, owner, contract.DesktopStatusStatusReady)
	client := &testSessionClient{
		snapshot: func(context.Context, uuid.UUID) (contract.Snapshot, error) {
			return contract.Snapshot{}, &transport.RequestError{Status: 503, Code: "state_unavailable"}
		},
		release: func(context.Context, uuid.UUID) error { return nil },
	}
	session := newDemoSession(context.Background(), client, uuid.New(), identity.BackendRunId)
	session.mu.Lock()
	session.leaseID = uuid.New()
	session.mu.Unlock()
	owner.mu.Lock()
	owner.session = session
	owner.mu.Unlock()
	defer session.close()
	if err := owner.Close(); !errors.Is(err, transport.ErrCleanupUnconfirmed) {
		t.Fatalf("unconfirmed session shutdown was hidden: %v", err)
	}
}

func TestVerificationFailureIsNotHiddenByDiagnosticChange(t *testing.T) {
	identity := contract.Handshake{ApiVersion: "v1", BackendRunId: uuid.New(), Demo: true}
	started, release := make(chan struct{}), make(chan struct{})
	core := &fakeCore{done: make(chan struct{}), degraded: make(chan struct{}), closed: make(chan struct{}), handshake: func(context.Context) (contract.Handshake, error) {
		close(started)
		<-release
		return contract.Handshake{}, errors.New("test failure")
	}}
	owner := newTestOwner(func(context.Context) (ownedCore, contract.Handshake, error) { return core, identity, nil })
	owner.Start()
	waitStatus(t, owner, contract.DesktopStatusStatusReady)
	result := make(chan contract.DesktopStatus, 1)
	go func() { result <- owner.Verify(context.Background()) }()
	<-started
	close(core.degraded)
	deadline := time.After(time.Second)
	for !owner.Status().DiagnosticsDegraded {
		select {
		case <-deadline:
			t.Fatal("diagnostic failure not observed")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	close(release)
	if status := <-result; status.Status != contract.DesktopStatusStatusUnavailable || status.Handshake != nil {
		t.Fatal("failed handshake remained confirmed")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledLaterVerificationDoesNotHideEarlierFailure(t *testing.T) {
	identity := contract.Handshake{ApiVersion: "v1", BackendRunId: uuid.New(), Demo: true}
	started, release := make(chan struct{}), make(chan struct{})
	core := &fakeCore{done: make(chan struct{}), degraded: make(chan struct{}), closed: make(chan struct{}), handshake: func(context.Context) (contract.Handshake, error) {
		close(started)
		<-release
		return contract.Handshake{}, errors.New("test failure")
	}}
	owner := newTestOwner(func(context.Context) (ownedCore, contract.Handshake, error) { return core, identity, nil })
	owner.Start()
	waitStatus(t, owner, contract.DesktopStatusStatusReady)
	first := make(chan contract.DesktopStatus, 1)
	go func() { first <- owner.Verify(context.Background()) }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = owner.Verify(ctx)
	close(release)
	if status := <-first; status.Status != contract.DesktopStatusStatusUnavailable {
		t.Fatal("canceled verification hid earlier failure")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseReportsCoreFailureAndObservesLateDiagnosticFailure(t *testing.T) {
	identity := contract.Handshake{ApiVersion: "v1", BackendRunId: uuid.New(), Demo: true}
	core := &fakeCore{done: make(chan struct{}), degraded: make(chan struct{}), closed: make(chan struct{}), closeErr: errors.New("core failed")}
	core.onClose = func() { close(core.degraded); close(core.done) }
	owner := newTestOwner(func(context.Context) (ownedCore, contract.Handshake, error) { return core, identity, nil })
	owner.Start()
	waitStatus(t, owner, contract.DesktopStatusStatusReady)
	if err := owner.Close(); err == nil {
		t.Fatal("core exit error was hidden")
	}
	if !owner.Status().DiagnosticsDegraded {
		t.Fatal("late diagnostic failure was hidden")
	}
}

func TestCloseBeforeStartDoesNotLaunchCore(t *testing.T) {
	called := false
	owner := newTestOwner(func(context.Context) (ownedCore, contract.Handshake, error) {
		called = true
		return nil, contract.Handshake{}, nil
	})
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	owner.Start()
	if called {
		t.Fatal("closed owner launched a core")
	}
}
