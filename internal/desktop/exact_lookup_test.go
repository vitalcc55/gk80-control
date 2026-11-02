package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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
	session.leaseID, session.leaseConfirmed, session.leaseUntil = lease, true, time.Now().Add(time.Minute)
	session.mu.Unlock()
	owner := &Owner{session: session, process: &fakeCore{done: make(chan struct{})}, databaseID: database, backendID: run, status: contract.DesktopStatus{HostRunId: host, Status: contract.DesktopStatusStatusReady}}
	service := NewService(owner)
	create := `{"commandId":"` + id.String() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1.0000000000000001,"pmaxPa":1000,"measurementSeconds":10,"direction":"pressure","actuator":false}}`
	start := `{"commandId":"` + id.String() + `","expectedRevision":1,"valveClosed":true,"connectionConfirmed":true}`
	confirm := `{"commandId":"` + id.String() + `","expectedRevision":2}`
	for _, tc := range []struct {
		kind, test, action, body string
		testUUID, actionUUID     uuid.UUID
	}{
		{"create", "", "", create, uuid.Nil, uuid.Nil},
		{"start", testID.String(), "", start, testID, uuid.Nil},
		{"confirm", testID.String(), actionID.String(), confirm, testID, actionID},
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
