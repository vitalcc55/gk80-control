package desktop

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

func TestBlockedConnectionEmitterDoesNotBlockStopOrFallback(t *testing.T) {
	owner := newTestOwner(nil)
	defer owner.cancel()
	testID, stopID := uuid.New(), uuid.New()
	client := &testSessionClient{stop: func(_ context.Context, _, id uuid.UUID, command contract.StopTestCommand) (contract.CommandStatus, error) {
		return contract.CommandStatus{CommandId: command.CommandId, TestId: &id, Status: contract.CommandStatusStatusCompleted}, nil
	}}
	session := newDemoSession(owner.ctx, client, uuid.New(), uuid.New())
	defer session.close()
	session.leaseID = uuid.New()
	session.activeTestID, session.activeStopID = testID, stopID
	session.onChange = owner.signalConnection
	owner.session = session
	owner.status.Status = contract.DesktopStatusStatusReady
	owner.process = &fakeCore{done: make(chan struct{})}
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	owner.SetConnectionEmitter(func(contract.ConnectionState) { close(entered); <-release })
	go owner.delivery.connectionLoop()
	if err := owner.EnableConnectionEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("connection emitter did not start")
	}
	result := make(chan error, 1)
	go func() {
		session.mu.Lock()
		session.changedLocked()
		session.mu.Unlock()
		notice, err := owner.WaitConnection(ctx, 0, 0)
		if err == nil && notice.Connection.Revision < 2 {
			err = errSessionUnavailable
		}
		if err == nil {
			status, stopErr := owner.StopTest(ctx, testID.String(), contract.StopTestCommand{CommandId: stopID})
			err = stopErr
			if err == nil && (status.CommandId != stopID || status.Status != contract.CommandStatusStatusCompleted) {
				err = errSessionUnavailable
			}
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("fallback or independent Stop failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("blocked emitter held fallback or Stop")
	}
}

func TestCoreEventEmitterCanAcknowledgeSynchronously(t *testing.T) {
	owner := newTestOwner(nil)
	defer owner.cancel()
	acknowledged := make(chan error, 1)
	owner.SetCoreEventEmitter(func(event contract.Event) {
		acknowledged <- owner.AcknowledgeCoreEvent(context.Background(), event.BackendRunId.String(), event.Sequence)
	})
	go owner.delivery.eventLoop()
	if err := owner.EnableConnectionEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	owner.observeCoreEvent(contract.Event{BackendRunId: uuid.New(), Sequence: 1})
	select {
	case err := <-acknowledged:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("emitter could not acknowledge its own event")
	}
}
