package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

type exactSessionClient struct {
	sessionClient
	lookup func(context.Context, string, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, []byte) (contract.CommandStatus, error)
}

func (c exactSessionClient) LookupCreateExact(ctx context.Context, lease, database, id uuid.UUID, body []byte) (contract.CommandStatus, error) {
	return c.lookup(ctx, "create", lease, database, uuid.Nil, uuid.Nil, id, body)
}
func (c exactSessionClient) LookupStartExact(ctx context.Context, lease, database, testID, id uuid.UUID, body []byte) (contract.CommandStatus, error) {
	return c.lookup(ctx, "start", lease, database, testID, uuid.Nil, id, body)
}
func (c exactSessionClient) LookupConfirmExact(ctx context.Context, lease, database, testID, actionID, id uuid.UUID, body []byte) (contract.CommandStatus, error) {
	return c.lookup(ctx, "confirm", lease, database, testID, actionID, id, body)
}
func (c exactSessionClient) LookupStopExact(ctx context.Context, lease, database, testID, id uuid.UUID, body []byte) (contract.CommandStatus, error) {
	return c.lookup(ctx, "stop", lease, database, testID, uuid.Nil, id, body)
}

func TestOwnerExactLookupPreservesAttemptAndSeparatesOutcomes(t *testing.T) {
	ctx := context.Background()
	host, run, database, lease, id, testID, actionID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	var calls atomic.Int32
	var expectedKind, expectedBody string
	var expectedTest, expectedAction uuid.UUID
	var failure error
	resultStatus := contract.CommandStatusStatusCompleted
	client := exactSessionClient{lookup: func(_ context.Context, kind string, gotLease, gotDatabase, gotTest, gotAction, gotID uuid.UUID, body []byte) (contract.CommandStatus, error) {
		calls.Add(1)
		if kind != expectedKind || gotLease != lease || gotDatabase != database || gotTest != expectedTest || gotAction != expectedAction || gotID != id || string(body) != expectedBody {
			t.Error("lookup changed the original attempt")
		}
		return contract.CommandStatus{CommandId: id, Status: resultStatus, TestId: &testID}, failure
	}}
	session := newDemoSession(ctx, client, host, run)
	defer session.close()
	session.mu.Lock()
	session.databaseID = database
	session.leaseID, session.leaseConfirmed, session.leaseUntil = lease, true, time.Now().Add(time.Minute)
	session.mu.Unlock()
	owner := &Owner{session: session, process: &fakeCore{done: make(chan struct{})}, databaseID: database, backendID: run, status: contract.DesktopStatus{HostRunId: host, Status: contract.DesktopStatusStatusReady}}
	service := NewService(owner)
	create := `{"commandId":"` + id.String() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1.0000000000000001,"pmaxPa":1000,"measurementSeconds":10,"direction":"pressure","actuator":false}}`
	start := `{"commandId":"` + id.String() + `","expectedRevision":1,"valveClosed":true,"connectionConfirmed":true}`
	confirm := `{"commandId":"` + id.String() + `","expectedRevision":2}`
	stop := `{"commandId":"` + id.String() + `"}`
	for _, tc := range []struct {
		kind, test, action, body string
		testUUID, actionUUID     uuid.UUID
	}{
		{"create", "", "", create, uuid.Nil, uuid.Nil},
		{"start", testID.String(), "", start, testID, uuid.Nil},
		{"confirm", testID.String(), actionID.String(), confirm, testID, actionID},
		{"stop", testID.String(), "", stop, testID, uuid.Nil},
	} {
		expectedKind, expectedBody, expectedTest, expectedAction = tc.kind, tc.body, tc.testUUID, tc.actionUUID
		result, err := service.LookupCommandExact(ctx, database.String(), tc.kind, tc.test, tc.action, id.String(), tc.body)
		if err != nil || result.Outcome != contract.ExactLookupResultOutcomeMatch || result.Command == nil || result.Command.CommandId != id {
			t.Fatalf("%s match: %+v %v", tc.kind, result, err)
		}
	}
	expectedKind, expectedBody, expectedTest, expectedAction = "create", create, uuid.Nil, uuid.Nil
	resultStatus = contract.CommandStatusStatusUnknown
	result, err := service.LookupCommandExact(ctx, database.String(), "create", "", "", id.String(), create)
	if err != nil || result.Outcome != contract.ExactLookupResultOutcomeUnknown || result.Command != nil {
		t.Fatalf("unknown lookup: %+v %v", result, err)
	}
	resultStatus = contract.CommandStatusStatusCompleted
	failure = &transport.RequestError{Status: http.StatusConflict, Code: "command_conflict"}
	result, err = service.LookupCommandExact(ctx, database.String(), "create", "", "", id.String(), create)
	if err != nil || result.Outcome != contract.ExactLookupResultOutcomeConflict || result.Command != nil {
		t.Fatalf("conflict: %+v %v", result, err)
	}
	failure = &transport.RequestError{Code: "unavailable"}
	if _, err := service.LookupCommandExact(ctx, database.String(), "create", "", "", id.String(), create); err == nil {
		t.Fatal("failed lookup reported a result")
	}
	before := calls.Load()
	result, err = service.LookupCommandExact(ctx, uuid.NewString(), "create", "", "", id.String(), create)
	if err != nil || result.Outcome != contract.ExactLookupResultOutcomeDatabaseMismatch || result.Command != nil || calls.Load() != before {
		t.Fatalf("database mismatch: %+v %v", result, err)
	}
	if _, err := service.LookupCommandExact(ctx, database.String(), "create", "", "", id.String(), `{"commandId":"`+id.String()+`"}`); err == nil || calls.Load() != before {
		t.Fatal("missing fields admitted")
	}
	if _, err := service.LookupCommandExact(ctx, database.String(), "create", "", "", uuid.NewString(), create); err == nil || calls.Load() != before {
		t.Fatal("mismatched command ID admitted")
	}
	if _, err := service.LookupCommandExact(ctx, database.String(), "create", "", "", id.String(), strings.Repeat("x", 8193)); err == nil || calls.Load() != before {
		t.Fatal("oversized request admitted")
	}
	session.mu.Lock()
	tracked := len(session.commands)
	session.mu.Unlock()
	if tracked != 0 {
		t.Fatal("read-only lookup changed command tracking")
	}
	session.mu.Lock()
	session.leaseUntil = time.Now().Add(-time.Second)
	session.mu.Unlock()
	if _, err := service.LookupCommandExact(ctx, uuid.NewString(), "create", "", "", id.String(), create); err == nil {
		t.Fatal("expired lease returned database mismatch")
	}
}

func TestReservedStopNeverUsesGenericCommandAsProof(t *testing.T) {
	host, run, database, lease, id, testID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	var generic atomic.Int32
	client := &testSessionClient{lookup: func(context.Context, uuid.UUID, uuid.UUID) (contract.CommandStatus, error) {
		generic.Add(1)
		return contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusCompleted}, nil
	}, lookupStop: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, []byte) (contract.CommandStatus, error) {
		return contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusUnknown}, nil
	}}
	session := newDemoSession(context.Background(), client, host, run)
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed, session.leaseUntil = lease, true, time.Now().Add(time.Minute)
	session.snapshot = &contract.Snapshot{BackendRunId: run, ActiveTest: &contract.TestRecord{TestId: testID, StopCommandId: id}}
	session.commands[id] = contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusUnknown}
	session.commandPayloads[id] = "stop:" + testID.String() + `:{"commandId":"` + id.String() + `"}`
	session.mu.Unlock()
	owner := &Owner{session: session, process: &fakeCore{done: make(chan struct{})}, databaseID: database, backendID: run, status: contract.DesktopStatus{HostRunId: host, Status: contract.DesktopStatusStatusReady}}
	status, err := owner.Command(context.Background(), id.String())
	if err != nil || status.Status != contract.CommandStatusStatusUnknown || generic.Load() != 0 {
		t.Fatalf("generic status used as Stop proof: %+v %v calls=%d", status, err, generic.Load())
	}
	session.reconcilePending(context.Background(), lease)
	session.mu.Lock()
	tracked := session.commands[id]
	session.mu.Unlock()
	if tracked.Status != contract.CommandStatusStatusUnknown || generic.Load() != 0 {
		t.Fatalf("generic reconciliation used as Stop proof: %+v calls=%d", tracked, generic.Load())
	}
}

func TestLateGenericLookupCannotBecomeStopProof(t *testing.T) {
	host, run, database, lease, id, testID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	client := &testSessionClient{lookup: func(context.Context, uuid.UUID, uuid.UUID) (contract.CommandStatus, error) {
		close(entered)
		<-release
		return contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusCompleted}, nil
	}}
	session := newDemoSession(context.Background(), client, host, run)
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed, session.leaseUntil = lease, true, time.Now().Add(time.Minute)
	session.mu.Unlock()
	owner := &Owner{session: session, process: &fakeCore{done: make(chan struct{})}, databaseID: database, backendID: run, status: contract.DesktopStatus{HostRunId: host, Status: contract.DesktopStatusStatusReady}}
	result := make(chan contract.CommandStatus, 1)
	go func() { status, _ := owner.Command(context.Background(), id.String()); result <- status }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("generic read did not start")
	}
	session.mu.Lock()
	session.activeTestID, session.activeStopID = testID, id
	session.commands[id] = contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusUnknown}
	session.commandPayloads[id] = "stop:" + testID.String() + `:{"commandId":"` + id.String() + `"}`
	session.mu.Unlock()
	releaseOnce.Do(func() { close(release) })
	select {
	case status := <-result:
		if status.Status != contract.CommandStatusStatusUnknown {
			t.Fatalf("late generic read became Stop proof: %+v", status)
		}
	case <-time.After(time.Second):
		t.Fatal("generic read did not return")
	}
}

func TestPendingStopIsReadByExactLookup(t *testing.T) {
	lease, database, id, testID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	var generic, exact atomic.Int32
	client := &testSessionClient{
		lookup: func(context.Context, uuid.UUID, uuid.UUID) (contract.CommandStatus, error) {
			generic.Add(1)
			return contract.CommandStatus{}, nil
		},
		lookupStop: func(_ context.Context, _, gotDatabase, gotTest, gotID uuid.UUID, _ []byte) (contract.CommandStatus, error) {
			exact.Add(1)
			if gotDatabase != database || gotTest != testID || gotID != id {
				t.Error("incorrect Stop identity")
			}
			return contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusCompleted}, nil
		},
	}
	session := newDemoSession(context.Background(), client, uuid.New(), uuid.New())
	defer session.close()
	session.mu.Lock()
	session.databaseID, session.leaseID, session.activeTestID, session.activeStopID = database, lease, testID, id
	session.leaseConfirmed, session.leaseUntil = true, time.Now().Add(time.Minute)
	session.commands[id] = contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusAccepted}
	session.commandPayloads[id] = "stop:" + testID.String() + `:{"commandId":"` + id.String() + `"}`
	session.mu.Unlock()
	owner := &Owner{session: session}
	status, err := owner.Command(context.Background(), id.String())
	if err != nil || status.Status != contract.CommandStatusStatusCompleted || exact.Load() != 1 || generic.Load() != 0 {
		t.Fatalf("Stop lookup: %+v %v exact=%d generic=%d", status, err, exact.Load(), generic.Load())
	}
}

func TestStopLookupDiscardsLateResponseAfterSessionCloses(t *testing.T) {
	lease, id, testID := uuid.New(), uuid.New(), uuid.New()
	entered, release := make(chan struct{}), make(chan struct{})
	client := &testSessionClient{lookupStop: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, []byte) (contract.CommandStatus, error) {
		close(entered)
		<-release
		return contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusCompleted}, nil
	}}
	session := newDemoSession(context.Background(), client, uuid.New(), uuid.New())
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed, session.leaseUntil = lease, true, time.Now().Add(time.Minute)
	session.commands[id] = contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusUnknown}
	session.commandPayloads[id] = "stop:" + testID.String() + `:{"commandId":"` + id.String() + `"}`
	session.mu.Unlock()
	owner := &Owner{session: session}
	done := make(chan error, 1)
	go func() { _, err := owner.Command(context.Background(), id.String()); done <- err }()
	<-entered
	session.close()
	close(release)
	if err := <-done; err == nil {
		t.Fatal("accepted a Stop result after session closed")
	}
}

func TestPendingStopDoesNotDelaySnapshot(t *testing.T) {
	run, lease, id, testID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	var exact atomic.Int32
	client := &testSessionClient{lookupStop: func(ctx context.Context, _, _, _, _ uuid.UUID, _ []byte) (contract.CommandStatus, error) {
		exact.Add(1)
		<-ctx.Done()
		return contract.CommandStatus{}, ctx.Err()
	}}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed, session.leaseUntil, session.lastUI = lease, true, time.Now().Add(time.Minute), time.Now()
	session.commands[id] = contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusAccepted}
	session.commandPayloads[id] = "stop:" + testID.String() + `:{"commandId":"` + id.String() + `"}`
	session.mu.Unlock()
	snapshot := contract.Snapshot{BackendRunId: run, ActiveTest: &contract.TestRecord{TestId: testID, StopCommandId: id, Stage: "finished", Outcome: "interrupted", SaveStatus: "pending"}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := session.acceptSnapshot(ctx, lease, snapshot); err != nil {
		t.Fatalf("snapshot was blocked by Stop lookup: %v", err)
	}
	view, err := session.snapshotCopy()
	if err != nil || view.ActiveTest == nil || view.ActiveTest.Outcome != "interrupted" || exact.Load() != 0 {
		t.Fatalf("snapshot=%+v lookup calls=%d error=%v", view, exact.Load(), err)
	}
}

func TestRejectedStopIsNotAutomaticallyResentOnShutdown(t *testing.T) {
	lease, id, testID := uuid.New(), uuid.New(), uuid.New()
	var sent atomic.Int32
	client := &testSessionClient{stop: func(context.Context, uuid.UUID, uuid.UUID, contract.StopTestCommand) (contract.CommandStatus, error) {
		sent.Add(1)
		return contract.CommandStatus{}, nil
	}}
	session := newDemoSession(context.Background(), client, uuid.New(), uuid.New())
	defer session.close()
	session.mu.Lock()
	session.leaseID = lease
	session.activeTestID, session.activeStopID = testID, id
	session.mu.Unlock()
	identity, err := commandIdentity("stop:"+testID.String(), contract.StopTestCommand{CommandId: id})
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.command(context.Background(), id, identity, true, func(context.Context, uuid.UUID) (contract.CommandStatus, error) {
		return contract.CommandStatus{}, &transport.RequestError{Status: http.StatusConflict, Code: "command_conflict"}
	})
	if err == nil {
		t.Fatal("expected conflict")
	}
	session.startShutdownStop(context.Background(), lease, &contract.TestRecord{TestId: testID, StopCommandId: id})
	if sent.Load() != 0 {
		t.Fatal("shutdown resent Stop after a definite conflict")
	}
}

func TestShutdownDoesNotUseGenericCommandAsStopProof(t *testing.T) {
	run, database, lease, id, testID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	record := &contract.TestRecord{TestId: testID, StopCommandId: id, Stage: "finished", SaveStatus: "saved"}
	snapshot := contract.Snapshot{BackendRunId: run, ActiveTest: record}
	var generic, exact atomic.Int32
	client := &testSessionClient{
		snapshot: func(context.Context, uuid.UUID) (contract.Snapshot, error) { return snapshot, nil },
		release:  func(context.Context, uuid.UUID) error { return nil },
		lookup: func(context.Context, uuid.UUID, uuid.UUID) (contract.CommandStatus, error) {
			generic.Add(1)
			return contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusCompleted}, nil
		},
		lookupStop: func(_ context.Context, _, gotDatabase, gotTest, gotID uuid.UUID, body []byte) (contract.CommandStatus, error) {
			exact.Add(1)
			if gotDatabase != database || gotTest != testID || gotID != id || string(body) != `{"commandId":"`+id.String()+`"}` {
				t.Error("incorrect Stop identity")
			}
			return contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatusUnknown}, nil
		},
	}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.databaseID, session.leaseID, session.snapshot = database, lease, &snapshot
	session.commands[id] = contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusUnknown}
	session.commandPayloads[id] = "stop:" + testID.String() + `:{"commandId":"` + id.String() + `"}`
	session.mu.Unlock()
	if err := session.shutdown(context.Background()); !errors.Is(err, ErrSessionShutdownUnconfirmed) {
		t.Fatalf("unknown Stop reported as confirmed: %v", err)
	}
	if exact.Load() != 1 || generic.Load() != 0 {
		t.Fatalf("unexpected lookups: exact=%d generic=%d", exact.Load(), generic.Load())
	}
}

func TestTrackedStopDoesNotUseGenericLookupAfterReservationEvicted(t *testing.T) {
	host, run, database, lease, id, testID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	var generic atomic.Int32
	client := &testSessionClient{lookup: func(context.Context, uuid.UUID, uuid.UUID) (contract.CommandStatus, error) {
		generic.Add(1)
		return contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusCompleted}, nil
	}, lookupStop: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, []byte) (contract.CommandStatus, error) {
		return contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusUnknown}, nil
	}}
	session := newDemoSession(context.Background(), client, host, run)
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed, session.leaseUntil = lease, true, time.Now().Add(time.Minute)
	session.commands[id] = contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusUnknown}
	session.commandPayloads[id] = "stop:" + testID.String() + `:{"commandId":"` + id.String() + `"}`
	session.mu.Unlock()
	owner := &Owner{session: session, process: &fakeCore{done: make(chan struct{})}, databaseID: database, backendID: run, status: contract.DesktopStatus{HostRunId: host, Status: contract.DesktopStatusStatusReady}}
	status, err := owner.Command(context.Background(), id.String())
	if err != nil || status.Status != contract.CommandStatusStatusUnknown || generic.Load() != 0 {
		t.Fatalf("evicted Stop was confirmed by ID: %+v %v calls=%d", status, err, generic.Load())
	}
}

func TestShutdownRechecksStopAfterPendingSave(t *testing.T) {
	run, database, lease, id, testID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	record := &contract.TestRecord{TestId: testID, StopCommandId: id, Stage: "finished", Outcome: "interrupted", SaveStatus: "pending"}
	snapshot := contract.Snapshot{BackendRunId: run, ActiveTest: record}
	var exact, sent atomic.Int32
	client := &testSessionClient{
		snapshot: func(context.Context, uuid.UUID) (contract.Snapshot, error) { return snapshot, nil },
		release:  func(context.Context, uuid.UUID) error { return nil },
		test: func(context.Context, uuid.UUID, uuid.UUID) (contract.TestRecord, error) {
			result := *record
			result.SaveStatus = "saved"
			return result, nil
		},
		stop: func(context.Context, uuid.UUID, uuid.UUID, contract.StopTestCommand) (contract.CommandStatus, error) {
			sent.Add(1)
			return contract.CommandStatus{}, nil
		},
		lookupStop: func(_ context.Context, _, gotDatabase, gotTest, gotID uuid.UUID, _ []byte) (contract.CommandStatus, error) {
			if gotDatabase != database || gotTest != testID || gotID != id {
				t.Error("incorrect Stop identity")
			}
			if exact.Add(1) == 1 {
				return contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusUnknown}, nil
			}
			return contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusCompleted}, nil
		},
	}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.databaseID, session.leaseID, session.snapshot = database, lease, &snapshot
	session.commands[id] = contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusAccepted}
	session.commandPayloads[id] = "stop:" + testID.String() + `:{"commandId":"` + id.String() + `"}`
	session.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := session.shutdown(ctx); err != nil {
		t.Fatalf("saved Stop reported unconfirmed: %v", err)
	}
	if exact.Load() < 2 || sent.Load() != 0 {
		t.Fatalf("lookups=%d, unexpected Stop sends=%d", exact.Load(), sent.Load())
	}
}

func TestActiveStopIsNotEvictedAndResentOnShutdown(t *testing.T) {
	lease, id, testID := uuid.New(), uuid.New(), uuid.New()
	var sends atomic.Int32
	client := &testSessionClient{stop: func(context.Context, uuid.UUID, uuid.UUID, contract.StopTestCommand) (contract.CommandStatus, error) {
		sends.Add(1)
		return contract.CommandStatus{}, nil
	}}
	session := newDemoSession(context.Background(), client, uuid.New(), uuid.New())
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.activeStopID, session.activeTestID = lease, id, testID
	session.commands[id] = contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusCompleted}
	session.commandPayloads[id] = "stop:" + testID.String() + `:{"commandId":"` + id.String() + `"}`
	for i := 0; i < 63; i++ {
		other := uuid.New()
		session.commands[other] = contract.CommandStatus{CommandId: other, Status: contract.CommandStatusStatusUnknown}
		session.commandPayloads[other] = "create:pending"
	}
	session.mu.Unlock()
	_, _ = session.command(context.Background(), uuid.New(), "create:new", false, func(context.Context, uuid.UUID) (contract.CommandStatus, error) { return contract.CommandStatus{}, nil })
	session.startShutdownStop(context.Background(), lease, &contract.TestRecord{TestId: testID, StopCommandId: id, Stage: "running"})
	if sends.Load() != 0 {
		t.Fatalf("shutdown resent evicted Stop: %d", sends.Load())
	}
}

func TestRejectedStopAfterNaturalFinishIsKnownAndNotRepeated(t *testing.T) {
	run, lease, id, testID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	record := &contract.TestRecord{TestId: testID, StopCommandId: id, Stage: "finished", Outcome: "completed", SaveStatus: "saved"}
	snapshot := contract.Snapshot{BackendRunId: run, ActiveTest: record}
	var sends atomic.Int32
	client := &testSessionClient{
		snapshot: func(context.Context, uuid.UUID) (contract.Snapshot, error) { return snapshot, nil },
		release:  func(context.Context, uuid.UUID) error { return nil },
		stop: func(context.Context, uuid.UUID, uuid.UUID, contract.StopTestCommand) (contract.CommandStatus, error) {
			sends.Add(1)
			return contract.CommandStatus{}, nil
		},
	}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.snapshot = lease, &snapshot
	session.mu.Unlock()
	identity, err := commandIdentity("stop:"+testID.String(), contract.StopTestCommand{CommandId: id})
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.command(context.Background(), id, identity, true, func(context.Context, uuid.UUID) (contract.CommandStatus, error) {
		return contract.CommandStatus{}, &transport.RequestError{Status: http.StatusConflict, Code: "invalid_state"}
	})
	if err == nil {
		t.Fatal("expected invalid_state")
	}
	if err := session.shutdown(context.Background()); err != nil {
		t.Fatalf("known rejection after natural completion reported unknown: %v", err)
	}
	if sends.Load() != 0 {
		t.Fatalf("shutdown repeated rejected Stop: %d", sends.Load())
	}
}

func TestShutdownStopRejectedAfterNaturalFinishIsNotRepeated(t *testing.T) {
	run, lease, id, testID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	var sends atomic.Int32
	client := &testSessionClient{
		snapshot: func(context.Context, uuid.UUID) (contract.Snapshot, error) {
			return contract.Snapshot{BackendRunId: run, ActiveTest: &contract.TestRecord{TestId: testID, StopCommandId: id, Stage: "finished", Outcome: "completed", SaveStatus: "saved"}}, nil
		},
		release: func(context.Context, uuid.UUID) error { return nil },
		stop: func(context.Context, uuid.UUID, uuid.UUID, contract.StopTestCommand) (contract.CommandStatus, error) {
			sends.Add(1)
			return contract.CommandStatus{}, &transport.RequestError{Status: http.StatusConflict, Code: "invalid_state"}
		},
	}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.activeTestID, session.activeStopID = lease, testID, id
	session.mu.Unlock()
	if err := session.shutdown(context.Background()); err != nil || sends.Load() != 1 {
		t.Fatalf("shutdown error=%v sends=%d", err, sends.Load())
	}
}

func TestStopCollisionPreservesUnknownOrdinaryOutcome(t *testing.T) {
	lease, id, testID := uuid.New(), uuid.New(), uuid.New()
	session := newDemoSession(context.Background(), &testSessionClient{}, uuid.New(), uuid.New())
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.activeTestID, session.activeStopID = lease, testID, id
	session.commands[id] = contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatusUnknown}
	session.commandPayloads[id], session.inFlight[id] = "create:pending", true
	session.mu.Unlock()
	identity, err := commandIdentity("stop:"+testID.String(), contract.StopTestCommand{CommandId: id})
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.command(context.Background(), id, identity, true, func(context.Context, uuid.UUID) (contract.CommandStatus, error) {
		return contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusAccepted}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	unresolved := session.displaced != nil
	session.mu.Unlock()
	if !unresolved {
		t.Fatal("Stop collision lost an unknown ordinary attempt")
	}
}

func TestOwnerExactLookupResolvesOnlySameTrackedAttempt(t *testing.T) {
	host, run, database, lease, id, testID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	var failure error
	client := exactSessionClient{lookup: func(context.Context, string, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, []byte) (contract.CommandStatus, error) {
		return contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatusCompleted, TestId: &testID}, failure
	}}
	session := newDemoSession(context.Background(), client, host, run)
	defer session.close()
	body := `{"commandId":"` + id.String() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":10,"direction":"pressure","actuator":false}}`
	var input CreateCommandInput
	if err := json.Unmarshal([]byte(body), &input); err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed, session.leaseUntil = lease, true, time.Now().Add(time.Minute)
	session.commands[id] = contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatusUnknown}
	session.commandPayloads[id] = "create:" + string(input.identity)
	session.ambiguous[id] = true
	session.inFlight[id] = true
	session.mu.Unlock()
	owner := &Owner{session: session, process: &fakeCore{done: make(chan struct{})}, databaseID: database, backendID: run, status: contract.DesktopStatus{HostRunId: host, Status: contract.DesktopStatusStatusReady}}
	lookup := func(original string) {
		t.Helper()
		result, err := owner.LookupCommandExact(context.Background(), database.String(), "create", "", "", id.String(), original)
		if err != nil || result.Outcome != contract.ExactLookupResultOutcomeMatch {
			t.Fatalf("exact lookup: %+v %v", result, err)
		}
	}
	if _, err := owner.LookupCommandExact(context.Background(), database.String(), "create", "", "", id.String(), body); !errors.Is(err, errSessionBusy) {
		t.Fatalf("in-flight attempt should remain unresolved: %v", err)
	}
	session.mu.Lock()
	if !session.ambiguous[id] || session.commands[id].Status != contract.CommandStatusStatusUnknown {
		t.Fatal("in-flight attempt was resolved")
	}
	delete(session.inFlight, id)
	session.mu.Unlock()
	lookup(strings.Replace(body, `"valve":"V"`, `"valve":"other"`, 1))
	session.mu.Lock()
	if !session.ambiguous[id] || session.commands[id].Status != contract.CommandStatusStatusUnknown {
		t.Fatal("different attempt was resolved")
	}
	session.mu.Unlock()
	lookup(body)
	session.mu.Lock()
	if session.ambiguous[id] || session.commands[id].Status != contract.CommandStatusStatusCompleted {
		session.mu.Unlock()
		t.Fatal("confirmed matching attempt remained unresolved")
	}
	session.commands[id] = contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatusUnknown}
	session.ambiguous[id] = true
	session.mu.Unlock()
	failure = &transport.RequestError{Status: http.StatusConflict, Code: "command_conflict"}
	conflict, err := owner.LookupCommandExact(context.Background(), database.String(), "create", "", "", id.String(), strings.Replace(body, `"valve":"V"`, `"valve":"other"`, 1))
	if err != nil || conflict.Outcome != contract.ExactLookupResultOutcomeConflict {
		t.Fatalf("other attempt conflict: %+v %v", conflict, err)
	}
	session.mu.Lock()
	if !session.ambiguous[id] {
		session.mu.Unlock()
		t.Fatal("conflict for different attempt cleared tracked attempt")
	}
	session.mu.Unlock()
	conflict, err = owner.LookupCommandExact(context.Background(), database.String(), "create", "", "", id.String(), body)
	if err != nil || conflict.Outcome != contract.ExactLookupResultOutcomeConflict {
		t.Fatalf("tracked conflict: %+v %v", conflict, err)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if _, exists := session.commands[id]; exists || session.ambiguous[id] || session.commandPayloads[id] != "" {
		t.Fatal("confirmed conflict left tracked attempt unresolved")
	}
}

func TestDisplacedAttemptClearsOnConfirmedRejection(t *testing.T) {
	run, lease, id, testID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	entered, release := make(chan struct{}), make(chan struct{})
	client := &testSessionClient{lookup: func(context.Context, uuid.UUID, uuid.UUID) (contract.CommandStatus, error) {
		return contract.CommandStatus{}, &transport.RequestError{Status: http.StatusNotFound, Code: "command_not_found"}
	}}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	stream := &testSessionStream{run: run, events: make(chan struct{}, 1), done: make(chan struct{})}
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed, session.leaseUntil = lease, true, time.Now().Add(time.Minute)
	session.lastUI, session.lastProbe = time.Now(), time.Now()
	session.streamActive, session.streamDone, session.stream = true, stream.done, stream
	session.snapshot = &contract.Snapshot{BackendRunId: run}
	session.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := session.command(context.Background(), id, "create:old", false, func(context.Context, uuid.UUID) (contract.CommandStatus, error) {
			close(entered)
			<-release
			return contract.CommandStatus{}, &transport.RequestError{Status: http.StatusConflict, Code: "command_conflict"}
		})
		done <- err
	}()
	<-entered
	session.mu.Lock()
	session.activeTestID, session.activeStopID = testID, id
	session.mu.Unlock()
	_, err := session.command(context.Background(), id, "stop:"+testID.String()+":body", true, func(context.Context, uuid.UUID) (contract.CommandStatus, error) {
		return contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusAccepted}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("expected rejected ordinary command")
	}
	session.mu.Lock()
	displaced := session.displaced != nil
	session.mu.Unlock()
	if displaced {
		t.Fatal("known rejection remained unresolved")
	}
}

func TestDisplacedAttemptClearsOnlyAfterItsExactLookup(t *testing.T) {
	host, run, database, lease, id, testID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	client := exactSessionClient{lookup: func(context.Context, string, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, []byte) (contract.CommandStatus, error) {
		return contract.CommandStatus{}, &transport.RequestError{Status: http.StatusConflict, Code: "command_conflict"}
	}}
	session := newDemoSession(context.Background(), client, host, run)
	defer session.close()
	body := `{"commandId":"` + id.String() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":10,"direction":"pressure","actuator":false}}`
	var input CreateCommandInput
	if err := json.Unmarshal([]byte(body), &input); err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed, session.leaseUntil = lease, true, time.Now().Add(time.Minute)
	session.displaced = &displacedAttempt{id: id, identity: "create:" + string(input.identity), inFlight: true}
	session.commands[id] = contract.CommandStatus{CommandId: id, TestId: &testID, Status: contract.CommandStatusStatusCompleted}
	session.commandPayloads[id] = "stop:" + testID.String() + `:{"commandId":"` + id.String() + `"}`
	session.mu.Unlock()
	owner := &Owner{session: session, process: &fakeCore{done: make(chan struct{})}, databaseID: database, backendID: run, status: contract.DesktopStatus{HostRunId: host, Status: contract.DesktopStatusStatusReady}}
	lookup := func(value string) (contract.ExactLookupResult, error) {
		return owner.LookupCommandExact(context.Background(), database.String(), "create", "", "", id.String(), value)
	}
	if _, err := lookup(body); !errors.Is(err, errSessionBusy) {
		t.Fatalf("in-flight attempt: %v", err)
	}
	session.mu.Lock()
	session.displaced.inFlight = false
	session.mu.Unlock()
	if _, err := lookup(strings.Replace(body, `"valve":"V"`, `"valve":"other"`, 1)); err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	stillDisplaced := session.displaced != nil
	session.mu.Unlock()
	if !stillDisplaced {
		t.Fatal("different attempt cleared displaced attempt")
	}
	result, err := lookup(body)
	if err != nil || result.Outcome != contract.ExactLookupResultOutcomeConflict {
		t.Fatalf("conflict: %+v %v", result, err)
	}
	session.mu.Lock()
	stillDisplaced = session.displaced != nil
	session.mu.Unlock()
	if stillDisplaced {
		t.Fatal("resolved attempt remained displaced")
	}
}

func TestOwnerExactLookupDiscardsResponseAfterSessionCloses(t *testing.T) {
	host, run, database, lease, id, testID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	entered, release := make(chan struct{}), make(chan struct{})
	client := exactSessionClient{lookup: func(context.Context, string, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, []byte) (contract.CommandStatus, error) {
		close(entered)
		<-release
		return contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatusCompleted, TestId: &testID}, nil
	}}
	session := newDemoSession(context.Background(), client, host, run)
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed, session.leaseUntil = lease, true, time.Now().Add(time.Minute)
	session.mu.Unlock()
	owner := &Owner{session: session, process: &fakeCore{done: make(chan struct{})}, databaseID: database, backendID: run, status: contract.DesktopStatus{HostRunId: host, Status: contract.DesktopStatusStatusReady}}
	body := `{"commandId":"` + id.String() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":10,"direction":"pressure","actuator":false}}`
	done := make(chan error, 1)
	go func() {
		_, err := owner.LookupCommandExact(context.Background(), database.String(), "create", "", "", id.String(), body)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("lookup did not start")
	}
	session.close()
	close(release)
	select {
	case err := <-done:
		if err == nil || errors.Is(err, ErrCommandConflict) {
			t.Fatalf("closed session returned result: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("lookup did not return")
	}
}
