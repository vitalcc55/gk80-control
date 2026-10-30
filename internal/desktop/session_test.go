package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/diagnostics"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

type testSessionClient struct {
	sessionClient
	mu        sync.Mutex
	acquires  int
	lease     uuid.UUID
	acquire   func(context.Context, uuid.UUID, *uuid.UUID) (contract.UiLease, error)
	run       uuid.UUID
	stream    *testSessionStream
	unknown   bool
	command   contract.CommandStatus
	lookup    func(context.Context, uuid.UUID, uuid.UUID) (contract.CommandStatus, error)
	reads     int
	open      func(context.Context, uuid.UUID, uuid.UUID) (sessionStream, contract.Snapshot, error)
	renew     func(context.Context, uuid.UUID) (contract.UiLease, error)
	snapshot  func(context.Context, uuid.UUID) (contract.Snapshot, error)
	stop      func(context.Context, uuid.UUID, uuid.UUID, contract.StopTestCommand) (contract.CommandStatus, error)
	test      func(context.Context, uuid.UUID, uuid.UUID) (contract.TestRecord, error)
	release   func(context.Context, uuid.UUID) error
	list      func(context.Context, uuid.UUID, contract.ListTestsParams) (contract.TestList, error)
	createRaw func(context.Context, uuid.UUID, uuid.UUID, []byte) (contract.CommandStatus, error)
}

func (c *testSessionClient) AcquireLease(ctx context.Context, client uuid.UUID, proof *uuid.UUID) (contract.UiLease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.acquires++
	if c.acquire != nil {
		return c.acquire(ctx, client, proof)
	}
	if c.unknown {
		return contract.UiLease{}, &transport.RequestError{Code: "unavailable", OutcomeUnknown: true}
	}
	return contract.UiLease{ClientId: client, LeaseId: c.lease, ExpiresAt: time.Now().Add(30 * time.Second)}, nil
}

func (c *testSessionClient) RenewLease(ctx context.Context, lease uuid.UUID) (contract.UiLease, error) {
	if c.renew != nil {
		return c.renew(ctx, lease)
	}
	return contract.UiLease{LeaseId: lease, ClientId: c.stream.clientID, ExpiresAt: time.Now().Add(30 * time.Second)}, nil
}

func (c *testSessionClient) Snapshot(ctx context.Context, lease uuid.UUID) (contract.Snapshot, error) {
	return c.snapshot(ctx, lease)
}

func (c *testSessionClient) StopTest(ctx context.Context, lease, id uuid.UUID, command contract.StopTestCommand) (contract.CommandStatus, error) {
	return c.stop(ctx, lease, id, command)
}

func (c *testSessionClient) Test(ctx context.Context, lease, id uuid.UUID) (contract.TestRecord, error) {
	return c.test(ctx, lease, id)
}

func (c *testSessionClient) ReleaseLease(ctx context.Context, lease uuid.UUID) error {
	return c.release(ctx, lease)
}

func (c *testSessionClient) ListTests(ctx context.Context, lease uuid.UUID, params contract.ListTestsParams) (contract.TestList, error) {
	return c.list(ctx, lease, params)
}

func (c *testSessionClient) CreateTestRaw(ctx context.Context, lease, id uuid.UUID, body []byte) (contract.CommandStatus, error) {
	return c.createRaw(ctx, lease, id, body)
}

func (c *testSessionClient) openStream(ctx context.Context, lease, run uuid.UUID) (sessionStream, contract.Snapshot, error) {
	if c.open != nil {
		return c.open(ctx, lease, run)
	}
	return c.stream, contract.Snapshot{ApiVersion: "v1", BackendRunId: c.run, Revision: 1, Measurements: []contract.Measurement{{Sequence: 1}}}, nil
}

func (c *testSessionClient) Command(ctx context.Context, lease, id uuid.UUID) (contract.CommandStatus, error) {
	if c.lookup != nil {
		return c.lookup(ctx, lease, id)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	if c.command.CommandId != id {
		return contract.CommandStatus{}, &transport.RequestError{Status: 404, Code: "command_not_found"}
	}
	return c.command, nil
}

type testSessionStream struct {
	mu            sync.Mutex
	clientID      uuid.UUID
	run           uuid.UUID
	sequence      int64
	pending       bool
	failSnapshots int
	events        chan struct{}
	done          chan struct{}
	once          sync.Once
}

func (s *testSessionStream) Next(ctx context.Context) (contract.Event, error) {
	select {
	case <-s.events:
		s.mu.Lock()
		s.pending = true
		s.mu.Unlock()
		return contract.Event{BackendRunId: s.run, Revision: 1, Type: contract.EventTypeStateChanged}, nil
	case <-ctx.Done():
		return contract.Event{}, ctx.Err()
	case <-s.done:
		return contract.Event{}, transport.ErrStreamClosed
	}
}

func (s *testSessionStream) Poll(ctx context.Context) (contract.Event, bool, error) {
	select {
	case <-ctx.Done():
		return contract.Event{}, false, ctx.Err()
	case <-s.done:
		return contract.Event{}, false, transport.ErrStreamClosed
	case <-s.events:
		s.mu.Lock()
		s.pending = true
		s.mu.Unlock()
		return contract.Event{BackendRunId: s.run, Revision: 1, Type: contract.EventTypeStateChanged}, true, nil
	default:
		return contract.Event{}, false, nil
	}
}

func (s *testSessionStream) Snapshot(context.Context) (contract.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failSnapshots > 0 {
		s.failSnapshots--
		return contract.Snapshot{}, &transport.RequestError{Status: 503, Code: "state_unavailable"}
	}
	s.pending = false
	return contract.Snapshot{ApiVersion: "v1", BackendRunId: s.run, Revision: 1, Measurements: []contract.Measurement{{Sequence: s.sequence}}}, nil
}

func (s *testSessionStream) Pending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending || len(s.events) != 0
}

func (s *testSessionStream) PendingSince() time.Time { return time.Time{} }

func (s *testSessionStream) Probe(context.Context) error { return nil }
func (s *testSessionStream) Done() <-chan struct{}       { return s.done }
func (s *testSessionStream) Close(context.Context) error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func waitSession(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-deadline:
			t.Fatal("session condition not reached")
		case <-ticker.C:
		}
	}
}

func TestDemoSessionUpdatesMeasurementsWithSameRevision(t *testing.T) {
	run := uuid.New()
	stream := &testSessionStream{run: run, events: make(chan struct{}, 1), done: make(chan struct{})}
	client := &testSessionClient{lease: uuid.New(), run: run, stream: stream}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	stream.clientID = session.clientID
	defer session.close()
	if err := session.keepAlive(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitSession(t, func() bool { return session.connectionState().Status == contract.Live })
	stream.mu.Lock()
	stream.sequence = 2
	stream.mu.Unlock()
	stream.events <- struct{}{}
	waitSession(t, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		return session.snapshot != nil && session.snapshot.Revision == 1 && session.snapshot.Measurements[0].Sequence == 2
	})
	before := session.connectionState()
	session.mu.Lock()
	session.lastUI = time.Now().Add(-uiHeartbeatFreshness)
	session.mu.Unlock()
	after := session.connectionState()
	if after.Status != contract.Disconnected || after.Revision <= before.Revision {
		t.Fatalf("expired UI was reported live: %+v", after)
	}
}

func TestSessionViewPairsFreshnessAndSnapshot(t *testing.T) {
	run := uuid.New()
	stream := &testSessionStream{run: run, events: make(chan struct{}, 1), done: make(chan struct{})}
	client := &testSessionClient{lease: uuid.New(), run: run, stream: stream}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	stream.clientID = session.clientID
	defer session.close()
	if err := session.keepAlive(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitSession(t, func() bool { return session.connectionState().Status == contract.Live })
	first, err := session.sessionView()
	if err != nil || first.Connection.Status != contract.Live || first.Snapshot == nil || first.ValidForMs <= 0 {
		t.Fatalf("live view: %+v, %v", first, err)
	}
	stream.mu.Lock()
	stream.sequence = 2
	stream.mu.Unlock()
	stream.events <- struct{}{}
	var second contract.SessionView
	waitSession(t, func() bool {
		second, err = session.sessionView()
		return err == nil && second.Connection.Status == contract.Live && second.Snapshot != nil && second.Snapshot.Measurements[0].Sequence == 2
	})
	if second.Snapshot.Revision != first.Snapshot.Revision || second.Connection.Revision <= first.Connection.Revision || first.Snapshot.Measurements[0].Sequence != 1 {
		t.Fatalf("view did not preserve ordered immutable measurements: first=%+v second=%+v", first, second)
	}
	session.mu.Lock()
	session.lastUI = time.Now().Add(-uiHeartbeatFreshness)
	session.mu.Unlock()
	stale, err := session.sessionView()
	if err != nil || stale.Connection.Status == contract.Live || stale.ValidForMs != 0 || stale.Snapshot == nil {
		t.Fatalf("stale view: %+v, %v", stale, err)
	}
}

type blockedPendingStream struct {
	*testSessionStream
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (s *blockedPendingStream) Pending() bool {
	s.once.Do(func() {
		close(s.entered)
		<-s.resume
	})
	return false
}

func TestOwnerSessionViewRejectsCoreExitDuringRead(t *testing.T) {
	core := &fakeCore{done: make(chan struct{})}
	owner := &Owner{process: core, status: contract.DesktopStatus{Status: contract.DesktopStatusStatusReady}}
	session := newDemoSession(context.Background(), &testSessionClient{}, uuid.New(), uuid.New())
	defer session.close()
	stream := &blockedPendingStream{testSessionStream: &testSessionStream{done: make(chan struct{})}, entered: make(chan struct{}), resume: make(chan struct{})}
	now := time.Now()
	session.leaseConfirmed = true
	session.leaseID = uuid.New()
	session.leaseUntil = now.Add(time.Minute)
	session.lastUI, session.lastProbe = now, now
	session.streamActive, session.streamDone, session.stream = true, stream.done, stream
	session.snapshot = &contract.Snapshot{Measurements: []contract.Measurement{}}
	owner.session = session
	result := make(chan error, 1)
	go func() {
		_, err := owner.SessionView(context.Background())
		result <- err
	}()
	<-stream.entered
	close(core.done)
	close(stream.resume)
	if err := <-result; err == nil {
		t.Fatal("view reported success after the core exited")
	}
}

func TestSessionViewUsesEarliestFreshnessDeadline(t *testing.T) {
	for _, earliest := range []string{"ui", "lease", "probe"} {
		t.Run(earliest, func(t *testing.T) {
			now := time.Now()
			stream := &testSessionStream{done: make(chan struct{})}
			session := newDemoSession(context.Background(), &testSessionClient{}, uuid.New(), uuid.New())
			defer session.close()
			session.leaseConfirmed = true
			session.leaseUntil = now.Add(5 * time.Second)
			session.lastUI = now.Add(-uiHeartbeatFreshness + 5*time.Second)
			session.lastProbe = now.Add(-streamProbeFreshness + 5*time.Second)
			session.streamActive, session.streamDone, session.stream = true, stream.done, stream
			session.snapshot = &contract.Snapshot{Measurements: []contract.Measurement{}}
			switch earliest {
			case "ui":
				session.lastUI = now.Add(-uiHeartbeatFreshness + time.Second)
			case "lease":
				session.leaseUntil = now.Add(time.Second)
			case "probe":
				session.lastProbe = now.Add(-streamProbeFreshness + time.Second)
			}
			view, err := session.sessionView()
			if err != nil || view.Connection.Status != contract.Live || view.ValidForMs <= 0 || view.ValidForMs > 1000 {
				t.Fatalf("earliest %s: %+v, %v", earliest, view, err)
			}
		})
	}
}

func TestDemoSessionDoesNotRepeatUnknownInitialLease(t *testing.T) {
	client := &testSessionClient{unknown: true}
	session := newDemoSession(context.Background(), client, uuid.New(), uuid.New())
	defer session.close()
	if err := session.keepAlive(context.Background()); err == nil {
		t.Fatal("lost first response was reported successful")
	}
	if err := session.keepAlive(context.Background()); err == nil {
		t.Fatal("unknown ownership was reported successful")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.acquires != 1 || session.connectionState().Status == contract.Live {
		t.Fatalf("unknown acquisition was retried: %d", client.acquires)
	}
}

func TestDemoSessionRejectsAdmissionExpiredWhileLocked(t *testing.T) {
	client := &testSessionClient{}
	session := newDemoSession(context.Background(), client, uuid.New(), uuid.New())
	defer session.close()
	session.mu.Lock()
	done := make(chan error, 1)
	deadline := time.Now().Add(25 * time.Millisecond)
	go func() { done <- session.keepAliveUntil(context.Background(), deadline) }()
	<-time.After(time.Until(deadline) + 10*time.Millisecond)
	session.mu.Unlock()
	if err := <-done; !errors.Is(err, errSessionUnavailable) {
		t.Fatalf("expired admission: %v", err)
	}
	if !session.lastUI.IsZero() || client.acquires != 0 {
		t.Fatal("expired admission changed UI freshness or requested a lease")
	}
}

func TestDemoSessionDoesNotCutOffAdmittedLeaseAtAdmissionDeadline(t *testing.T) {
	run := uuid.New()
	stream := &testSessionStream{run: run, events: make(chan struct{}, 1), done: make(chan struct{})}
	client := &testSessionClient{lease: uuid.New(), run: run, stream: stream}
	client.acquire = func(ctx context.Context, id uuid.UUID, _ *uuid.UUID) (contract.UiLease, error) {
		select {
		case <-time.After(100 * time.Millisecond):
			return contract.UiLease{ClientId: id, LeaseId: client.lease, ExpiresAt: time.Now().Add(30 * time.Second)}, nil
		case <-ctx.Done():
			return contract.UiLease{}, ctx.Err()
		}
	}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	stream.clientID = session.clientID
	defer session.close()
	if err := session.keepAliveUntil(context.Background(), time.Now().Add(50*time.Millisecond)); err != nil {
		t.Fatalf("admitted lease was cut short: %v", err)
	}
}

func TestDemoSessionRecoversAfterStreamLoss(t *testing.T) {
	run := uuid.New()
	first := &testSessionStream{run: run, events: make(chan struct{}, 1), done: make(chan struct{})}
	second := &testSessionStream{run: run, events: make(chan struct{}, 1), done: make(chan struct{})}
	client := &testSessionClient{lease: uuid.New(), run: run, stream: first}
	var mu sync.Mutex
	opens := 0
	client.open = func(context.Context, uuid.UUID, uuid.UUID) (sessionStream, contract.Snapshot, error) {
		mu.Lock()
		defer mu.Unlock()
		opens++
		stream := first
		if opens > 1 {
			stream = second
		}
		return stream, contract.Snapshot{ApiVersion: "v1", BackendRunId: run, Revision: 1, Measurements: []contract.Measurement{{Sequence: int64(opens)}}}, nil
	}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	if err := session.keepAlive(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitSession(t, func() bool { return session.connectionState().Status == contract.Live })
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state := session.connectionState(); state.Status == contract.Live {
		t.Fatalf("closed stream stayed live: %+v", state)
	}
	waitSession(t, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		return session.snapshot != nil && session.snapshot.Measurements[0].Sequence == 2
	})
	waitSession(t, func() bool { return session.connectionState().Status == contract.Live })
}

func TestDemoSessionWaitsForTemporarilyUnavailableSnapshot(t *testing.T) {
	run := uuid.New()
	stream := &testSessionStream{run: run, sequence: 2, failSnapshots: 1, events: make(chan struct{}, 1), done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snapshot, err := readSessionSnapshot(ctx, stream)
	if err != nil || snapshot.BackendRunId != run || snapshot.Measurements[0].Sequence != 2 {
		t.Fatalf("temporary snapshot failure: %+v %v", snapshot, err)
	}
}

func TestDemoSessionReacquiresAfterConfirmedInvalidLease(t *testing.T) {
	run := uuid.New()
	stream := &testSessionStream{run: run, events: make(chan struct{}, 1), done: make(chan struct{})}
	client := &testSessionClient{lease: uuid.New(), run: run, stream: stream}
	client.renew = func(context.Context, uuid.UUID) (contract.UiLease, error) {
		return contract.UiLease{}, &transport.RequestError{Status: 403, Code: "lease_invalid", OutcomeUnknown: true}
	}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.leaseID = uuid.New()
	session.leaseUntil = time.Now().Add(-time.Second)
	session.mu.Unlock()
	if err := session.keepAlive(context.Background()); err == nil {
		t.Fatal("expired lease was reported renewed")
	}
	if err := session.keepAlive(context.Background()); err != nil {
		t.Fatalf("confirmed invalid lease was not replaced: %v", err)
	}
	waitSession(t, func() bool { return session.connectionState().Status == contract.Live })
}

func TestDemoSessionStopsRenewingAfterRecoveryBudget(t *testing.T) {
	client := &testSessionClient{lease: uuid.New()}
	client.open = func(context.Context, uuid.UUID, uuid.UUID) (sessionStream, contract.Snapshot, error) {
		return nil, contract.Snapshot{}, transport.ErrStreamClosed
	}
	session := newDemoSession(context.Background(), client, uuid.New(), uuid.New())
	defer session.close()
	if err := session.keepAlive(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitSession(t, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		return session.streamBroken
	})
	if err := session.keepAlive(context.Background()); err == nil {
		t.Fatal("failed recovery continued renewing the lease")
	}
}

func TestDemoSessionRecoversAfterFailedStreamLeaseExpires(t *testing.T) {
	run, oldLease, newLease := uuid.New(), uuid.New(), uuid.New()
	stream := &testSessionStream{run: run, events: make(chan struct{}, 1), done: make(chan struct{})}
	client := &testSessionClient{lease: newLease, run: run, stream: stream}
	client.acquire = func(_ context.Context, clientID uuid.UUID, proof *uuid.UUID) (contract.UiLease, error) {
		if proof != nil {
			if *proof != oldLease {
				t.Error("wrong old lease proof")
			}
			return contract.UiLease{}, &transport.RequestError{Status: 403, Code: "lease_invalid", OutcomeUnknown: true}
		}
		return contract.UiLease{ClientId: clientID, LeaseId: newLease, ExpiresAt: time.Now().Add(30 * time.Second)}, nil
	}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.leaseID = oldLease
	session.streamBroken = true
	session.mu.Unlock()
	if err := session.keepAlive(context.Background()); err == nil {
		t.Fatal("expired proof was treated as a new lease")
	}
	if err := session.keepAlive(context.Background()); err != nil {
		t.Fatalf("new lease after confirmed expiration: %v", err)
	}
	waitSession(t, func() bool { return session.connectionState().Status == contract.Live })
}

func TestDemoSessionShutdownSendsStopBeforeBlockedSnapshot(t *testing.T) {
	run, lease, id, stopID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	stopped, unblock := make(chan struct{}), make(chan struct{})
	client := &testSessionClient{}
	client.stop = func(_ context.Context, gotLease, gotID uuid.UUID, command contract.StopTestCommand) (contract.CommandStatus, error) {
		if gotLease != lease || gotID != id || command.CommandId != stopID {
			t.Error("stop used the wrong identifiers")
		}
		close(stopped)
		return contract.CommandStatus{CommandId: stopID, TestId: &id, Status: contract.CommandStatusStatusAccepted}, nil
	}
	client.snapshot = func(ctx context.Context, _ uuid.UUID) (contract.Snapshot, error) {
		select {
		case <-unblock:
			return contract.Snapshot{ApiVersion: "v1", BackendRunId: run, ActiveTest: &contract.TestRecord{TestId: id, StopCommandId: stopID, Stage: "finished", SaveStatus: "saved"}}, nil
		case <-ctx.Done():
			return contract.Snapshot{}, ctx.Err()
		}
	}
	client.command = contract.CommandStatus{CommandId: stopID, Status: contract.CommandStatusStatusCompleted}
	client.release = func(context.Context, uuid.UUID) error { return nil }
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.leaseID = lease
	session.activeTestID, session.activeStopID = id, stopID
	session.commands[stopID] = contract.CommandStatus{CommandId: stopID, Status: contract.CommandStatusStatusUnknown}
	session.commandPayloads[stopID] = "ordinary"
	session.inFlight[stopID] = true
	session.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.shutdown(ctx) }()
	select {
	case <-stopped:
	case <-ctx.Done():
		close(unblock)
		t.Fatal("stop waited for snapshot")
	}
	close(unblock)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDemoSessionShutdownStopsNewlyObservedTestBeforePendingReads(t *testing.T) {
	run, lease, id, stopID, pendingID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	stopped, unblock := make(chan struct{}), make(chan struct{})
	client := &testSessionClient{}
	client.snapshot = func(context.Context, uuid.UUID) (contract.Snapshot, error) {
		return contract.Snapshot{ApiVersion: "v1", BackendRunId: run, ActiveTest: &contract.TestRecord{TestId: id, StopCommandId: stopID, Stage: "running"}}, nil
	}
	client.stop = func(context.Context, uuid.UUID, uuid.UUID, contract.StopTestCommand) (contract.CommandStatus, error) {
		close(stopped)
		return contract.CommandStatus{CommandId: stopID, TestId: &id, Status: contract.CommandStatusStatusAccepted}, nil
	}
	client.lookup = func(ctx context.Context, _, commandID uuid.UUID) (contract.CommandStatus, error) {
		if commandID == pendingID {
			select {
			case <-unblock:
			case <-ctx.Done():
				return contract.CommandStatus{}, ctx.Err()
			}
		}
		return contract.CommandStatus{CommandId: commandID, Status: contract.CommandStatusStatusCompleted}, nil
	}
	client.test = func(context.Context, uuid.UUID, uuid.UUID) (contract.TestRecord, error) {
		return contract.TestRecord{TestId: id, Stage: "finished", SaveStatus: "saved"}, nil
	}
	client.release = func(context.Context, uuid.UUID) error { return nil }
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.leaseID = lease
	session.snapshot = &contract.Snapshot{BackendRunId: run}
	session.commands[pendingID] = contract.CommandStatus{CommandId: pendingID, Status: contract.CommandStatusStatusUnknown}
	session.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.shutdown(ctx) }()
	select {
	case <-stopped:
	case <-ctx.Done():
		close(unblock)
		t.Fatal("newly observed test was not stopped before pending reads")
	}
	close(unblock)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDemoSessionReconcilesPendingWithoutBlockingStop(t *testing.T) {
	run, lease, id := uuid.New(), uuid.New(), uuid.New()
	client := &testSessionClient{}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed = lease, true
	session.leaseUntil = time.Now().Add(time.Minute)
	session.lastUI, session.lastProbe = time.Now(), time.Now()
	session.streamActive = true
	session.streamDone = make(chan struct{})
	session.stream = &testSessionStream{events: make(chan struct{}, 1), done: make(chan struct{})}
	session.snapshot = &contract.Snapshot{ApiVersion: "v1", BackendRunId: run, Measurements: []contract.Measurement{}}
	session.mu.Unlock()
	started, release := make(chan struct{}), make(chan struct{})
	result := make(chan contract.CommandStatus, 1)
	go func() {
		status, _ := session.command(context.Background(), id, "create", false, func(context.Context, uuid.UUID) (contract.CommandStatus, error) {
			close(started)
			<-release
			return contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatusAccepted}, nil
		})
		result <- status
	}()
	<-started
	client.mu.Lock()
	client.command = contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatusCompleted}
	client.mu.Unlock()
	stopID := uuid.New()
	stopped, err := session.command(context.Background(), stopID, "stop", true, func(context.Context, uuid.UUID) (contract.CommandStatus, error) {
		return contract.CommandStatus{CommandId: stopID, Status: contract.CommandStatusStatusAccepted}, nil
	})
	if err != nil || stopped.Status != contract.CommandStatusStatusAccepted {
		t.Fatalf("stop waited for ordinary command: %+v %v", stopped, err)
	}
	if err := session.acceptSnapshot(context.Background(), lease, contract.Snapshot{ApiVersion: "v1", BackendRunId: run, Measurements: []contract.Measurement{}}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if status := <-result; status.Status != contract.CommandStatusStatusAccepted {
		t.Fatalf("in-flight command was resolved before its response: %+v", status)
	}
	if err := session.acceptSnapshot(context.Background(), lease, contract.Snapshot{ApiVersion: "v1", BackendRunId: run, Measurements: []contract.Measurement{}}); err != nil {
		t.Fatal(err)
	}
	called := false
	status, err := session.command(context.Background(), id, "create", false, func(context.Context, uuid.UUID) (contract.CommandStatus, error) {
		called = true
		return contract.CommandStatus{}, nil
	})
	if err != nil || called || status.Status != contract.CommandStatusStatusCompleted {
		t.Fatalf("command was repeated: %+v %v", status, err)
	}
	if _, err := session.command(context.Background(), id, "different", false, nil); err == nil {
		t.Fatal("identifier was accepted with different content")
	}
}

func TestDemoSessionReconcilesLostResponseWithoutResending(t *testing.T) {
	run, lease, id := uuid.New(), uuid.New(), uuid.New()
	client := &testSessionClient{}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed = lease, true
	session.leaseUntil = time.Now().Add(time.Minute)
	session.lastUI, session.lastProbe = time.Now(), time.Now()
	session.streamActive = true
	session.streamDone = make(chan struct{})
	session.stream = &testSessionStream{events: make(chan struct{}, 1), done: make(chan struct{})}
	session.snapshot = &contract.Snapshot{ApiVersion: "v1", BackendRunId: run, Measurements: []contract.Measurement{}}
	session.mu.Unlock()
	sends := 0
	send := func(context.Context, uuid.UUID) (contract.CommandStatus, error) {
		sends++
		return contract.CommandStatus{}, &transport.RequestError{Code: "unavailable", OutcomeUnknown: true}
	}
	status, err := session.command(context.Background(), id, "create", false, send)
	if err == nil || status.Status != contract.CommandStatusStatusUnknown {
		t.Fatalf("lost response: %+v %v", status, err)
	}
	client.mu.Lock()
	client.command = contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatusCompleted}
	client.mu.Unlock()
	if err := session.acceptSnapshot(context.Background(), lease, contract.Snapshot{ApiVersion: "v1", BackendRunId: run, Measurements: []contract.Measurement{}}); err != nil {
		t.Fatal(err)
	}
	status, err = session.command(context.Background(), id, "create", false, send)
	if err != nil || status.Status != contract.CommandStatusStatusCompleted || sends != 1 {
		t.Fatalf("reconciled command: %+v %v, sends=%d", status, err, sends)
	}
}

func TestDemoSessionDoesNotAttributeOldCommandToConflict(t *testing.T) {
	run, lease, id := uuid.New(), uuid.New(), uuid.New()
	client := &testSessionClient{command: contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatusCompleted}}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed = lease, true
	session.leaseUntil = time.Now().Add(time.Minute)
	session.lastUI, session.lastProbe = time.Now(), time.Now()
	session.streamActive = true
	session.streamDone = make(chan struct{})
	session.stream = &testSessionStream{events: make(chan struct{}, 1), done: make(chan struct{})}
	session.snapshot = &contract.Snapshot{ApiVersion: "v1", BackendRunId: run, Measurements: []contract.Measurement{}}
	session.mu.Unlock()
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := session.command(context.Background(), id, "different content", false, func(context.Context, uuid.UUID) (contract.CommandStatus, error) {
			close(started)
			<-release
			return contract.CommandStatus{}, &transport.RequestError{Status: 409, Code: "command_conflict"}
		})
		done <- err
	}()
	<-started
	if err := session.acceptSnapshot(context.Background(), lease, contract.Snapshot{ApiVersion: "v1", BackendRunId: run, Measurements: []contract.Measurement{}}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("different command reused a previous result")
	}
	session.mu.Lock()
	_, retained := session.commands[id]
	session.mu.Unlock()
	if retained {
		t.Fatal("conflicting old result remained in the command registry")
	}
}

func TestOwnerCommandUpdatesTrackedUnknownWithoutEvent(t *testing.T) {
	id, lease := uuid.New(), uuid.New()
	client := &testSessionClient{command: contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatusRejected}}
	session := newDemoSession(context.Background(), client, uuid.New(), uuid.New())
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed = lease, true
	session.leaseUntil = time.Now().Add(time.Minute)
	session.commands[id] = contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatusUnknown}
	session.commandPayloads[id] = "start"
	session.mu.Unlock()
	owner := &Owner{session: session, status: contract.DesktopStatus{Status: contract.DesktopStatusStatusReady}}
	status, err := owner.Command(context.Background(), id.String())
	if err != nil || status.Status != contract.CommandStatusStatusRejected {
		t.Fatalf("command lookup: %+v %v", status, err)
	}
	session.mu.Lock()
	tracked := session.commands[id]
	session.mu.Unlock()
	if tracked.Status != contract.CommandStatusStatusRejected {
		t.Fatalf("command registry stayed unknown: %+v", tracked)
	}
}

func TestOwnerForwardsHistoryCursor(t *testing.T) {
	lease, after := uuid.New(), uuid.New()
	limit := 1
	client := &testSessionClient{list: func(_ context.Context, gotLease uuid.UUID, params contract.ListTestsParams) (contract.TestList, error) {
		if gotLease != lease || params.After == nil || *params.After != after || params.Limit == nil || *params.Limit != limit {
			t.Error("history cursor was not forwarded")
		}
		return contract.TestList{Items: []contract.TestRecord{}}, nil
	}}
	session := newDemoSession(context.Background(), client, uuid.New(), uuid.New())
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed = lease, true
	session.leaseUntil = time.Now().Add(time.Minute)
	session.mu.Unlock()
	owner := &Owner{session: session, status: contract.DesktopStatus{Status: contract.DesktopStatusStatusReady}}
	if _, err := owner.ListTests(context.Background(), contract.ListTestsParams{After: &after, Limit: &limit}); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerCommandDoesNotDowngradeAcceptedStop(t *testing.T) {
	id, lease, testID := uuid.New(), uuid.New(), uuid.New()
	entered, release := make(chan struct{}), make(chan struct{})
	client := &testSessionClient{lookup: func(context.Context, uuid.UUID, uuid.UUID) (contract.CommandStatus, error) {
		close(entered)
		<-release
		return contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusUnknown}, nil
	}}
	session := newDemoSession(context.Background(), client, uuid.New(), uuid.New())
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed = lease, true
	session.leaseUntil = time.Now().Add(time.Minute)
	session.commands[id] = contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusUnknown}
	session.mu.Unlock()
	owner := &Owner{session: session, status: contract.DesktopStatus{Status: contract.DesktopStatusStatusReady}}
	done := make(chan contract.CommandStatus, 1)
	go func() { status, _ := owner.Command(context.Background(), id.String()); done <- status }()
	<-entered
	session.mu.Lock()
	session.commands[id] = contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusAccepted}
	session.mu.Unlock()
	close(release)
	if status := <-done; status.Status != contract.CommandStatusStatusAccepted {
		t.Fatalf("late unknown replaced accepted stop: %+v", status)
	}
}

func TestOwnerLateHistoryDoesNotReplaceActiveStop(t *testing.T) {
	first, second, lease := uuid.New(), uuid.New(), uuid.New()
	firstStop, secondStop := uuid.New(), uuid.New()
	entered, release := make(chan struct{}), make(chan struct{})
	client := &testSessionClient{}
	client.test = func(context.Context, uuid.UUID, uuid.UUID) (contract.TestRecord, error) {
		close(entered)
		<-release
		return contract.TestRecord{TestId: first, StopCommandId: firstStop, Stage: "running"}, nil
	}
	session := newDemoSession(context.Background(), client, uuid.New(), uuid.New())
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed = lease, true
	session.leaseUntil = time.Now().Add(time.Minute)
	session.rememberStopLocked(contract.TestRecord{TestId: first, StopCommandId: firstStop, Stage: "running"})
	session.mu.Unlock()
	owner := &Owner{session: session, status: contract.DesktopStatus{Status: contract.DesktopStatusStatusReady}}
	done := make(chan error, 1)
	go func() { _, err := owner.Test(context.Background(), first.String()); done <- err }()
	<-entered
	session.mu.Lock()
	session.rememberStopLocked(contract.TestRecord{TestId: second, StopCommandId: secondStop, Stage: "running"})
	session.mu.Unlock()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	active, stopID := session.activeTestID, session.activeStopID
	session.mu.Unlock()
	if active != second || stopID != secondStop {
		t.Fatal("late history response replaced the newer active test")
	}
}

func TestOwnerFinishedSnapshotOverridesOlderStoredStage(t *testing.T) {
	id, stopID, lease := uuid.New(), uuid.New(), uuid.New()
	client := &testSessionClient{test: func(context.Context, uuid.UUID, uuid.UUID) (contract.TestRecord, error) {
		return contract.TestRecord{TestId: id, StopCommandId: stopID, Stage: "running"}, nil
	}}
	session := newDemoSession(context.Background(), client, uuid.New(), uuid.New())
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed = lease, true
	session.leaseUntil = time.Now().Add(time.Minute)
	session.snapshot = &contract.Snapshot{ActiveTest: &contract.TestRecord{TestId: id, StopCommandId: stopID, Stage: "finished"}}
	session.mu.Unlock()
	owner := &Owner{session: session, status: contract.DesktopStatus{Status: contract.DesktopStatusStatusReady}}
	if _, err := owner.Test(context.Background(), id.String()); err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	active := session.activeTestID
	session.mu.Unlock()
	if active != uuid.Nil {
		t.Fatal("stale stored stage restored a finished test")
	}
}

func TestDemoSessionDoesNotSendAfterPreflightLosesUI(t *testing.T) {
	run, lease, id := uuid.New(), uuid.New(), uuid.New()
	entered, release := make(chan struct{}), make(chan struct{})
	client := &testSessionClient{}
	client.lookup = func(context.Context, uuid.UUID, uuid.UUID) (contract.CommandStatus, error) {
		close(entered)
		<-release
		return contract.CommandStatus{}, &transport.RequestError{Status: 404, Code: "command_not_found"}
	}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed = lease, true
	session.leaseUntil = time.Now().Add(time.Minute)
	session.lastUI, session.lastProbe = time.Now(), time.Now()
	session.streamActive = true
	session.streamDone = make(chan struct{})
	session.stream = &testSessionStream{events: make(chan struct{}, 1), done: make(chan struct{})}
	session.snapshot = &contract.Snapshot{ApiVersion: "v1", BackendRunId: run, Measurements: []contract.Measurement{}}
	session.mu.Unlock()
	sent := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := session.command(context.Background(), id, "start", false, func(context.Context, uuid.UUID) (contract.CommandStatus, error) {
			sent <- struct{}{}
			return contract.CommandStatus{}, nil
		})
		done <- err
	}()
	<-entered
	session.mu.Lock()
	session.lastUI = time.Now().Add(-uiHeartbeatFreshness)
	session.mu.Unlock()
	close(release)
	if err := <-done; err == nil {
		t.Fatal("command was admitted after UI expiry")
	}
	select {
	case <-sent:
		t.Fatal("command was sent after UI expiry")
	default:
	}
}

func TestDemoSessionActionContextExpiresWithUI(t *testing.T) {
	run, lease, id := uuid.New(), uuid.New(), uuid.New()
	client := &testSessionClient{}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed = lease, true
	session.leaseUntil = time.Now().Add(time.Minute)
	session.lastUI = time.Now().Add(-uiHeartbeatFreshness + 150*time.Millisecond)
	session.lastProbe = time.Now()
	session.streamActive = true
	session.streamDone = make(chan struct{})
	session.stream = &testSessionStream{events: make(chan struct{}, 1), done: make(chan struct{})}
	session.snapshot = &contract.Snapshot{BackendRunId: run, Measurements: []contract.Measurement{}}
	session.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := session.command(context.Background(), id, "start", false, func(ctx context.Context, _ uuid.UUID) (contract.CommandStatus, error) {
			<-ctx.Done()
			return contract.CommandStatus{}, ctx.Err()
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expired UI action reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("action outlived UI freshness")
	}
}

func TestDemoSessionRenewalFailureCancelsAdmittedAction(t *testing.T) {
	run, lease, id := uuid.New(), uuid.New(), uuid.New()
	client := &testSessionClient{renew: func(context.Context, uuid.UUID) (contract.UiLease, error) {
		return contract.UiLease{}, &transport.RequestError{Code: "unavailable", OutcomeUnknown: true}
	}}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed = lease, true
	session.leaseUntil = time.Now().Add(time.Minute)
	session.lastUI, session.lastProbe = time.Now(), time.Now()
	session.streamActive = true
	session.streamDone = make(chan struct{})
	session.stream = &testSessionStream{events: make(chan struct{}, 1), done: make(chan struct{})}
	session.snapshot = &contract.Snapshot{BackendRunId: run, Measurements: []contract.Measurement{}}
	session.mu.Unlock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := session.command(context.Background(), id, "start", false, func(ctx context.Context, _ uuid.UUID) (contract.CommandStatus, error) {
			close(started)
			<-ctx.Done()
			return contract.CommandStatus{}, ctx.Err()
		})
		done <- err
	}()
	<-started
	if err := session.keepAlive(context.Background()); err == nil {
		t.Fatal("failed renewal reported success")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("action remained confirmed after lost lease confirmation")
		}
	case <-time.After(time.Second):
		t.Fatal("renewal failure did not cancel action")
	}
}

func TestDemoSessionDoesNotTreatInvalid404AsMissingCommand(t *testing.T) {
	id := uuid.New()
	client := &testSessionClient{lookup: func(context.Context, uuid.UUID, uuid.UUID) (contract.CommandStatus, error) {
		return contract.CommandStatus{}, &transport.RequestError{Status: 404, Code: "invalid_response"}
	}}
	session := newDemoSession(context.Background(), client, uuid.New(), uuid.New())
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed = uuid.New(), true
	session.leaseUntil = time.Now().Add(time.Minute)
	session.lastUI, session.lastProbe = time.Now(), time.Now()
	session.streamActive = true
	session.streamDone = make(chan struct{})
	session.stream = &testSessionStream{events: make(chan struct{}, 1), done: make(chan struct{})}
	session.snapshot = &contract.Snapshot{Measurements: []contract.Measurement{}}
	session.mu.Unlock()
	sent := false
	_, err := session.command(context.Background(), id, "create", false, func(context.Context, uuid.UUID) (contract.CommandStatus, error) {
		sent = true
		return contract.CommandStatus{}, nil
	})
	if err == nil || sent {
		t.Fatal("malformed lookup allowed a command")
	}
}

func TestOwnerStopUsesPreviouslyReadRecordWithoutSnapshot(t *testing.T) {
	testID, stopID, lease := uuid.New(), uuid.New(), uuid.New()
	client := &testSessionClient{}
	client.stop = func(_ context.Context, gotLease, gotTest uuid.UUID, cmd contract.StopTestCommand) (contract.CommandStatus, error) {
		if gotLease != lease || gotTest != testID || cmd.CommandId != stopID {
			t.Error("wrong stop identifiers")
		}
		return contract.CommandStatus{CommandId: stopID, TestId: &testID, Status: contract.CommandStatusStatusAccepted}, nil
	}
	session := newDemoSession(context.Background(), client, uuid.New(), uuid.New())
	defer session.close()
	session.mu.Lock()
	session.leaseID = lease
	session.rememberStopLocked(contract.TestRecord{TestId: testID, StopCommandId: stopID})
	session.commands[stopID] = contract.CommandStatus{CommandId: stopID, Status: contract.CommandStatusStatusUnknown}
	session.commandPayloads[stopID] = "ordinary"
	session.inFlight[stopID] = true
	session.mu.Unlock()
	owner := &Owner{session: session, status: contract.DesktopStatus{Status: contract.DesktopStatusStatusReady}}
	status, err := owner.StopTest(context.Background(), testID.String(), contract.StopTestCommand{CommandId: stopID})
	if err != nil || status.Status != contract.CommandStatusStatusAccepted {
		t.Fatalf("stop was blocked by stale snapshot or ordinary command: %+v %v", status, err)
	}
}

func TestOwnerExplicitlyRepeatsUnknownStop(t *testing.T) {
	testID, stopID, lease := uuid.New(), uuid.New(), uuid.New()
	client := &testSessionClient{}
	sends := 0
	client.stop = func(context.Context, uuid.UUID, uuid.UUID, contract.StopTestCommand) (contract.CommandStatus, error) {
		sends++
		if sends == 1 {
			return contract.CommandStatus{}, &transport.RequestError{Code: "unavailable", OutcomeUnknown: true}
		}
		return contract.CommandStatus{CommandId: stopID, TestId: &testID, Status: contract.CommandStatusStatusAccepted}, nil
	}
	session := newDemoSession(context.Background(), client, uuid.New(), uuid.New())
	defer session.close()
	session.mu.Lock()
	session.leaseID = lease
	session.rememberStopLocked(contract.TestRecord{TestId: testID, StopCommandId: stopID, Stage: "running"})
	session.mu.Unlock()
	owner := &Owner{session: session, status: contract.DesktopStatus{Status: contract.DesktopStatusStatusReady}}
	command := contract.StopTestCommand{CommandId: stopID}
	if _, err := owner.StopTest(context.Background(), testID.String(), command); err == nil {
		t.Fatal("lost stop response was reported confirmed")
	}
	status, err := owner.StopTest(context.Background(), testID.String(), command)
	if err != nil || status.Status != contract.CommandStatusStatusAccepted || sends != 2 {
		t.Fatalf("explicit stop retry: %+v %v, sends=%d", status, err, sends)
	}
}

func TestOwnerUnknownStopKeepsPinnedTestAfterHistoryEviction(t *testing.T) {
	run, testID, stopID := uuid.New(), uuid.New(), uuid.New()
	client := &testSessionClient{stop: func(context.Context, uuid.UUID, uuid.UUID, contract.StopTestCommand) (contract.CommandStatus, error) {
		return contract.CommandStatus{}, &transport.RequestError{Code: "unavailable", OutcomeUnknown: true}
	}}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed = uuid.New(), true
	session.leaseUntil = time.Now().Add(time.Minute)
	session.lastUI, session.lastProbe = time.Now(), time.Now()
	session.streamActive = true
	session.streamDone = make(chan struct{})
	session.stream = &testSessionStream{events: make(chan struct{}, 1), done: make(chan struct{})}
	session.snapshot = &contract.Snapshot{BackendRunId: run, ActiveTest: &contract.TestRecord{TestId: testID, StopCommandId: stopID, Stage: "running"}, Measurements: []contract.Measurement{}}
	session.activeTestID, session.activeStopID = testID, stopID
	session.mu.Unlock()
	owner := &Owner{session: session, status: contract.DesktopStatus{Status: contract.DesktopStatusStatusReady}}
	status, err := owner.StopTest(context.Background(), testID.String(), contract.StopTestCommand{CommandId: stopID})
	if err == nil || status.TestId == nil || *status.TestId != testID {
		t.Fatalf("unknown stop lost test identity: %+v %v", status, err)
	}
	sent := false
	_, err = session.command(context.Background(), uuid.New(), "start", false, func(context.Context, uuid.UUID) (contract.CommandStatus, error) {
		sent = true
		return contract.CommandStatus{}, nil
	})
	if err == nil || sent {
		t.Fatal("ordinary command was admitted while stop is unresolved")
	}
}

func TestDemoSessionFinishedUnknownStopDoesNotBlockNewCommand(t *testing.T) {
	run, lease, oldTest, oldStop, newID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	client := &testSessionClient{}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed = lease, true
	session.leaseUntil = time.Now().Add(time.Minute)
	session.lastUI, session.lastProbe = time.Now(), time.Now()
	session.streamActive = true
	session.streamDone = make(chan struct{})
	session.stream = &testSessionStream{events: make(chan struct{}, 1), done: make(chan struct{})}
	session.snapshot = &contract.Snapshot{BackendRunId: run, ActiveTest: &contract.TestRecord{TestId: oldTest, StopCommandId: oldStop, Stage: "finished"}, Measurements: []contract.Measurement{}}
	session.commands[oldStop] = contract.CommandStatus{CommandId: oldStop, TestId: &oldTest, Status: contract.CommandStatusStatusUnknown}
	session.commandPayloads[oldStop] = "stop:" + oldTest.String()
	session.mu.Unlock()
	status, err := session.command(context.Background(), newID, "create", false, func(context.Context, uuid.UUID) (contract.CommandStatus, error) {
		return contract.CommandStatus{CommandId: newID, Status: contract.CommandStatusStatusCompleted}, nil
	})
	if err != nil || status.Status != contract.CommandStatusStatusCompleted {
		t.Fatalf("finished test blocked new command: %+v %v", status, err)
	}
}

func TestDemoSessionMeasuresLiveWhileStopIsPending(t *testing.T) {
	testID, stopID := uuid.New(), uuid.New()
	session := newDemoSession(context.Background(), &testSessionClient{}, uuid.New(), uuid.New())
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed = uuid.New(), true
	session.leaseUntil = time.Now().Add(time.Minute)
	session.lastUI, session.lastProbe = time.Now(), time.Now()
	session.streamActive = true
	session.streamDone = make(chan struct{})
	session.stream = &testSessionStream{events: make(chan struct{}, 1), done: make(chan struct{})}
	session.snapshot = &contract.Snapshot{Measurements: []contract.Measurement{}}
	session.activeTestID = testID
	session.commands[stopID] = contract.CommandStatus{CommandId: stopID, TestId: &testID, Status: contract.CommandStatusStatusAccepted}
	session.commandPayloads[stopID] = "stop:" + testID.String()
	session.startLiveLocked(time.Now())
	marked := !session.liveMark.IsZero()
	commandAllowed := session.commandAllowedLocked(false)
	session.mu.Unlock()
	if !marked || commandAllowed {
		t.Fatal("pending stop confused connection health with command availability")
	}
}

func TestOwnerReturnsKnownStopAfterFinishedTest(t *testing.T) {
	testID, stopID := uuid.New(), uuid.New()
	session := newDemoSession(context.Background(), &testSessionClient{}, uuid.New(), uuid.New())
	defer session.close()
	identity, err := commandIdentity("stop:"+testID.String(), contract.StopTestCommand{CommandId: stopID})
	if err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	session.commands[stopID] = contract.CommandStatus{CommandId: stopID, TestId: &testID, Status: contract.CommandStatusStatusCompleted}
	session.commandPayloads[stopID] = identity
	session.snapshot = &contract.Snapshot{ActiveTest: &contract.TestRecord{TestId: testID, StopCommandId: stopID, Stage: "finished"}}
	session.mu.Unlock()
	owner := &Owner{session: session, status: contract.DesktopStatus{Status: contract.DesktopStatusStatusReady}}
	status, err := owner.StopTest(context.Background(), testID.String(), contract.StopTestCommand{CommandId: stopID})
	if err != nil || status.Status != contract.CommandStatusStatusCompleted {
		t.Fatalf("known finished stop: %+v %v", status, err)
	}
}

func TestOwnerSessionRunsAndReopensCore(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "gk80-core")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	buildCtx, buildCancel := context.WithTimeout(context.Background(), time.Minute)
	defer buildCancel()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", executable, "./cmd/gk80-core")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build core: %v %s", err, output)
	}
	profile := t.TempDir()
	hostID := uuid.New()
	diagnostic, err := diagnostics.Open(filepath.Join(profile, "logs"), diagnostics.RoleDesktop, hostID, io.Discard, diagnostics.Config{QueueSize: 64, MaxBytes: 1 << 20, AppVersion: "0.1.0", ProfileID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = diagnostic.Close(context.Background()) }()
	logger := diagnostic.Application(diagnostics.ComponentTransport)
	newOwner := func() *Owner {
		owner := NewOwner(uuid.New(), executable, profile, "test", "0.1.0", logger, nil, false, func(contract.DesktopStatus) {})
		owner.Start()
		waitStatus(t, owner, contract.DesktopStatusStatusReady)
		if err := owner.KeepAlive(context.Background()); err != nil {
			t.Fatal(err)
		}
		waitSession(t, func() bool {
			state, err := owner.ConnectionState(context.Background())
			return err == nil && state.Status == contract.Live
		})
		return owner
	}
	owner := newOwner()
	t.Cleanup(func() { _ = owner.Close() })
	snapshot, err := owner.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	create := contract.CreateTestCommand{CommandId: uuid.New(), ExpectedRevision: snapshot.Revision, Parameters: contract.TestParameters{Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 10, Direction: "pressure"}}
	service := NewService(owner)
	body, err := json.Marshal(create)
	if err != nil {
		t.Fatal(err)
	}
	var createInput CreateCommandInput
	if err := json.Unmarshal(body, &createInput); err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateTest(context.Background(), createInput)
	if err != nil || created.TestId == nil || created.Status != contract.CommandStatusStatusCompleted {
		t.Fatalf("create: %+v %v", created, err)
	}
	id := created.TestId.String()
	ready, err := owner.Readiness(context.Background(), id)
	if err != nil || !ready.Ready {
		t.Fatalf("readiness: %+v %v", ready, err)
	}
	waitSession(t, func() bool {
		state, stateErr := owner.ConnectionState(context.Background())
		return stateErr == nil && state.Status == contract.Live
	})
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: ready.Revision, ValveClosed: true, ConnectionConfirmed: true}
	body, err = json.Marshal(start)
	if err != nil {
		t.Fatal(err)
	}
	var startInput StartCommandInput
	if err := json.Unmarshal(body, &startInput); err != nil {
		t.Fatal(err)
	}
	started, err := service.StartTest(context.Background(), id, startInput)
	if err != nil || started.Status != contract.CommandStatusStatusAccepted {
		t.Fatalf("start: %+v %v", started, err)
	}
	waitSession(t, func() bool {
		snapshot, err = owner.Snapshot(context.Background())
		state, stateErr := owner.ConnectionState(context.Background())
		return err == nil && stateErr == nil && state.Status == contract.Live && snapshot.PendingAction != nil
	})
	confirm := contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: snapshot.Revision}
	body, err = json.Marshal(confirm)
	if err != nil {
		t.Fatal(err)
	}
	var confirmInput ConfirmCommandInput
	if err := json.Unmarshal(body, &confirmInput); err != nil {
		t.Fatal(err)
	}
	confirmed, err := service.ConfirmAction(context.Background(), id, snapshot.PendingAction.ActionId.String(), confirmInput)
	if err != nil || confirmed.Status != contract.CommandStatusStatusCompleted {
		session, _ := owner.currentSession()
		session.mu.Lock()
		remaining, uiAge, probeAge, active, broken := time.Until(session.leaseUntil), time.Since(session.lastUI), time.Since(session.lastProbe), session.streamActive, session.streamBroken
		session.mu.Unlock()
		t.Fatalf("confirm: %+v %#v; lease=%v ui=%v probe=%v active=%v broken=%v", confirmed, err, remaining, uiAge, probeAge, active, broken)
	}
	waitSession(t, func() bool {
		snapshot, err = owner.Snapshot(context.Background())
		return err == nil && len(snapshot.Measurements) > 0 && snapshot.ActiveTest != nil
	})
	if snapshot.Measurements[0].Source != "simulator" {
		t.Fatalf("source: %+v", snapshot.Measurements[0])
	}
	stopID := snapshot.ActiveTest.StopCommandId
	body, err = json.Marshal(contract.StopTestCommand{CommandId: stopID})
	if err != nil {
		t.Fatal(err)
	}
	var stopInput StopCommandInput
	if err := json.Unmarshal(body, &stopInput); err != nil {
		t.Fatal(err)
	}
	stopped, err := service.StopTest(context.Background(), id, stopInput)
	if err != nil || stopped.Status != contract.CommandStatusStatusAccepted {
		t.Fatalf("stop: %+v %v", stopped, err)
	}
	waitSession(t, func() bool {
		stopped, err = owner.Command(context.Background(), stopID.String())
		return err == nil && stopped.Status == contract.CommandStatusStatusCompleted
	})
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	owner = newOwner()
	history, err := owner.ListTests(context.Background(), contract.ListTestsParams{})
	if err != nil || len(history.Items) != 1 || history.Items[0].TestId.String() != id {
		t.Fatalf("reopened history: %+v %v", history, err)
	}
	record, err := owner.Test(context.Background(), id)
	if err != nil || record.Outcome != "interrupted" || record.SaveStatus != "saved" {
		t.Fatalf("reopened: %+v %v", record, err)
	}
	measurements, err := owner.Measurements(context.Background(), id, 0, 10)
	if err != nil || len(measurements.Items) == 0 {
		t.Fatalf("reopened measurements: %+v %v", measurements, err)
	}
	audit, err := owner.Audit(context.Background(), id, 0, 10)
	if err != nil || len(audit.Items) == 0 {
		t.Fatalf("reopened audit: %+v %v", audit, err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := diagnostic.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	logs, err := os.ReadDir(filepath.Join(profile, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	var hostSent, hostObserved, coreFound bool
	for _, entry := range logs {
		data, err := os.ReadFile(filepath.Join(profile, "logs", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var record struct {
				Event     string `json:"event"`
				CommandID string `json:"command_id"`
				TestID    string `json:"test_id"`
			}
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatal(err)
			}
			if record.CommandID != create.CommandId.String() {
				continue
			}
			hostSent = hostSent || strings.HasPrefix(entry.Name(), "desktop-") && record.Event == "command.sent"
			hostObserved = hostObserved || strings.HasPrefix(entry.Name(), "desktop-") && record.Event == "command.observed" && record.TestID == id
			coreFound = coreFound || strings.HasPrefix(entry.Name(), "core-") && record.Event == "command.completed" && record.TestID == id
		}
	}
	if !hostSent || !hostObserved || !coreFound {
		t.Fatalf("command correlation missing: sent=%v observed=%v core=%v", hostSent, hostObserved, coreFound)
	}
}
