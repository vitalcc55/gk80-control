package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

func TestNativeCommandUnknownOutcomeIsNotRetried(t *testing.T) {
	commandID, leaseID := uuid.New(), uuid.New()
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-GK80-UI-Lease") != leaseID.String() {
			t.Error("missing native credentials")
		}
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"lease_invalid","message":"UI lease required"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(contract.CommandStatus{CommandId: commandID, Status: "completed"})
	}))
	defer server.Close()
	client := &Client{endpoint: server.URL, secret: "secret", http: server.Client()}
	_, err := client.CreateTest(context.Background(), leaseID, contract.CreateTestCommand{CommandId: commandID})
	var failure *RequestError
	if !errors.As(err, &failure) || !failure.OutcomeUnknown || posts.Load() != 1 {
		t.Fatalf("outcome: %v, posts: %d", err, posts.Load())
	}
	status, err := client.Command(context.Background(), leaseID, commandID)
	if err != nil || status.CommandId != commandID || status.Status != "completed" || posts.Load() != 1 {
		t.Fatalf("recovery: %+v %v, posts: %d", status, err, posts.Load())
	}
}

func TestNativeLeaseGateBusyIsDefiniteForNewRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"lease_operation_in_progress","message":"Another UI lease operation is in progress"}`))
	}))
	defer server.Close()
	client := &Client{endpoint: server.URL, secret: "secret", http: server.Client()}
	_, err := client.RenewLease(context.Background(), uuid.New())
	var failure *RequestError
	if !errors.As(err, &failure) || failure.Code != "lease_operation_in_progress" || failure.OutcomeUnknown {
		t.Fatalf("busy outcome: %v", err)
	}
}

func TestNativeClientRunsAndReopensDemo(t *testing.T) {
	ctx := context.Background()
	profileDir := t.TempDir()
	executable := buildCore(t)
	process, handshake, err := Start(ctx, executable, profileDir, "test", "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close(ctx) })
	client := process.client
	lease, err := client.AcquireLease(ctx, uuid.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := client.RenewLease(ctx, lease.LeaseId)
	if err != nil || renewed.LeaseId != lease.LeaseId || renewed.ClientId != lease.ClientId {
		t.Fatalf("renew: %+v %v", renewed, err)
	}
	recovered, err := client.AcquireLease(ctx, lease.ClientId, &lease.LeaseId)
	if err != nil || recovered.LeaseId != lease.LeaseId {
		t.Fatalf("recover lease: %+v %v", recovered, err)
	}
	command := contract.CreateTestCommand{CommandId: uuid.New(), ExpectedRevision: 0, Parameters: contract.TestParameters{Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 10, Direction: "pressure"}}
	created, err := client.CreateTest(ctx, lease.LeaseId, command)
	if err != nil || created.Status != "completed" || created.TestId == nil {
		t.Fatalf("create: %+v %v", created, err)
	}
	original, err := json.Marshal(command)
	if err != nil || !strings.Contains(string(original), `"areaM2":1`) {
		t.Fatalf("create JSON: %s %v", original, err)
	}
	equivalent := strings.Replace(string(original), `"areaM2":1`, `"areaM2":1.00`, 1)
	replayed, err := client.CreateTestRaw(ctx, lease.LeaseId, command.CommandId, []byte(equivalent))
	if err != nil || replayed.TestId == nil || *replayed.TestId != *created.TestId {
		t.Fatalf("equivalent raw replay: %+v %v", replayed, err)
	}
	different := strings.Replace(string(original), `"areaM2":1`, `"areaM2":1.0000000000000001`, 1)
	_, err = client.CreateTestRaw(ctx, lease.LeaseId, command.CommandId, []byte(different))
	var conflict *RequestError
	if !errors.As(err, &conflict) || conflict.Status != http.StatusConflict || conflict.Code != "command_conflict" {
		t.Fatalf("raw numeric identity was lost: %#v", err)
	}
	id := *created.TestId
	ready, err := client.Readiness(ctx, lease.LeaseId, id)
	if err != nil || !ready.Ready {
		t.Fatalf("readiness: %+v %v", ready, err)
	}
	started, err := client.StartTest(ctx, lease.LeaseId, id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: ready.Revision, ValveClosed: true, ConnectionConfirmed: true})
	if err != nil || started.Status != "accepted" {
		t.Fatalf("start: %+v %v", started, err)
	}
	snapshot, err := client.Snapshot(ctx, lease.LeaseId)
	if err != nil || snapshot.BackendRunId != handshake.BackendRunId || snapshot.PendingAction == nil || snapshot.ActiveTest == nil {
		t.Fatalf("snapshot: %+v %v", snapshot, err)
	}
	confirmed, err := client.ConfirmAction(ctx, lease.LeaseId, id, snapshot.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: snapshot.Revision})
	if err != nil || confirmed.Status != "completed" {
		t.Fatalf("confirm: %+v %v", confirmed, err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		snapshot, err = client.Snapshot(ctx, lease.LeaseId)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Measurements) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no simulator measurements")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if snapshot.Measurements[0].Source != "simulator" {
		t.Fatalf("source: %+v", snapshot.Measurements[0])
	}
	stopID := snapshot.ActiveTest.StopCommandId
	stopped, err := client.StopTest(ctx, lease.LeaseId, id, contract.StopTestCommand{CommandId: stopID})
	if err != nil || stopped.Status != "accepted" {
		t.Fatalf("stop: %+v %v", stopped, err)
	}
	for {
		stopped, err = client.Command(ctx, lease.LeaseId, stopID)
		if err != nil {
			t.Fatal(err)
		}
		if stopped.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stop not completed: %+v", stopped)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := process.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, nextHandshake, err := Start(ctx, executable, profileDir, "test", "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close(ctx) })
	if nextHandshake.BackendRunId == handshake.BackendRunId {
		t.Fatal("run identity reused")
	}
	if handshake.DatabaseId == uuid.Nil || nextHandshake.DatabaseId != handshake.DatabaseId {
		t.Fatalf("database identity changed across restart: %s -> %s", handshake.DatabaseId, nextHandshake.DatabaseId)
	}
	client = reopened.client
	lease, err = client.AcquireLease(ctx, uuid.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	record, err := client.Test(ctx, lease.LeaseId, id)
	if err != nil || record.Outcome != "interrupted" || record.SaveStatus != "saved" || record.Parameters.Valve != "V" {
		t.Fatalf("record: %+v %v", record, err)
	}
	page, err := client.Measurements(ctx, lease.LeaseId, id, 0, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].Source != "simulator" {
		t.Fatalf("measurements: %+v %v", page, err)
	}
	audit, err := client.Audit(ctx, lease.LeaseId, id, 0, 1)
	if err != nil || len(audit.Items) != 1 {
		t.Fatalf("audit: %+v %v", audit, err)
	}
	list, err := client.ListTests(ctx, lease.LeaseId, contract.ListTestsParams{})
	if err != nil || len(list.Items) != 1 || list.Items[0].TestId != id || list.NextAfter != nil {
		t.Fatalf("list: %+v %v", list, err)
	}
	snapshot, err = client.Snapshot(ctx, lease.LeaseId)
	if err != nil {
		t.Fatal(err)
	}
	command.CommandId = uuid.New()
	command.ExpectedRevision = snapshot.Revision
	command.Parameters.Valve = "V2"
	second, err := client.CreateTest(ctx, lease.LeaseId, command)
	if err != nil || second.TestId == nil {
		t.Fatalf("second create: %+v %v", second, err)
	}
	limit := 1
	firstPage, err := client.ListTests(ctx, lease.LeaseId, contract.ListTestsParams{Limit: &limit})
	if err != nil || len(firstPage.Items) != 1 || firstPage.NextAfter == nil {
		t.Fatalf("first page: %+v %v", firstPage, err)
	}
	secondPage, err := client.ListTests(ctx, lease.LeaseId, contract.ListTestsParams{After: firstPage.NextAfter, Limit: &limit})
	if err != nil || len(secondPage.Items) != 1 || secondPage.NextAfter != nil || firstPage.Items[0].TestId == secondPage.Items[0].TestId {
		t.Fatalf("second page: %+v %v", secondPage, err)
	}
	if firstPage.Items[0].TestId != id && firstPage.Items[0].TestId != *second.TestId || secondPage.Items[0].TestId != id && secondPage.Items[0].TestId != *second.TestId {
		t.Fatal("unexpected history IDs")
	}
	if err := client.ReleaseLease(ctx, lease.LeaseId); err != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestNativeClientDoesNotRetryLostCommandResponse(t *testing.T) {
	commandID, leaseID := uuid.New(), uuid.New()
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		_ = json.NewEncoder(w).Encode(contract.CommandStatus{CommandId: commandID, Status: "completed"})
	}))
	defer server.Close()
	client := &Client{endpoint: server.URL, secret: "secret", http: server.Client()}
	_, err := client.CreateTest(context.Background(), leaseID, contract.CreateTestCommand{CommandId: commandID})
	var failure *RequestError
	if !errors.As(err, &failure) || !failure.OutcomeUnknown || posts.Load() != 1 {
		t.Fatalf("outcome: %v posts: %d", err, posts.Load())
	}
	status, err := client.Command(context.Background(), leaseID, commandID)
	if err != nil || status.Status != "completed" || posts.Load() != 1 {
		t.Fatalf("recovery: %+v %v posts: %d", status, err, posts.Load())
	}
}

func TestNativeClientBoundsAndValidatesPages(t *testing.T) {
	testID := uuid.New()
	var large atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if large.Load() {
			_, _ = w.Write([]byte(strings.Repeat("x", maxCoreResponseBytes+1)))
			return
		}
		page := contract.AuditPage{Items: make([]contract.AuditEvent, 500)}
		for index := range page.Items {
			page.Items[index] = contract.AuditEvent{Sequence: int64(index + 1), TestId: testID, OccurredAt: time.Now().UTC(), OperatorId: "demo", Action: "sample", Outcome: "completed"}
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()
	client := &Client{endpoint: server.URL, secret: "secret", http: server.Client()}
	page, err := client.Audit(context.Background(), uuid.New(), testID, 0, 500)
	if err != nil || len(page.Items) != 500 {
		t.Fatalf("page: %d %v", len(page.Items), err)
	}
	large.Store(true)
	if _, err := client.Audit(context.Background(), uuid.New(), testID, 0, 500); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestNativeClientRejectsMismatchedCommandAndCursors(t *testing.T) {
	testID, commandID, leaseID := uuid.New(), uuid.New(), uuid.New()
	next := int64(100)
	other := uuid.New()
	record := contract.TestRecord{TestId: testID, StopCommandId: uuid.New(), Demo: true, OperatorId: "demo", Parameters: contract.TestParameters{Valve: "V", AreaM2: 1, PmaxPa: 1, MeasurementSeconds: 1, Direction: "pressure"}, Stage: "preparing", Outcome: "pending", DataQuality: "pending", SaveStatus: "saved", CreatedAt: time.Now().UTC()}
	for _, tc := range []struct {
		name string
		body any
		call func(*Client) error
	}{
		{"command test", contract.CommandAccepted{CommandId: commandID, Status: "accepted", TestId: &other}, func(c *Client) error {
			_, err := c.StartTest(context.Background(), leaseID, testID, contract.StartTestCommand{CommandId: commandID})
			return err
		}},
		{"measurement cursor", contract.MeasurementPage{Items: []contract.Measurement{{Sequence: 1, Channel: "dd1", Quantity: "pressure", Unit: "Pa", ObservedAt: time.Now().UTC(), Quality: "unavailable", Source: "simulator"}}, NextAfter: &next}, func(c *Client) error {
			_, err := c.Measurements(context.Background(), leaseID, testID, 0, 1)
			return err
		}},
		{"audit owner", contract.AuditPage{Items: []contract.AuditEvent{{Sequence: 1, TestId: other, OccurredAt: time.Now().UTC(), OperatorId: "demo", Action: "stop", Outcome: "completed"}}}, func(c *Client) error { _, err := c.Audit(context.Background(), leaseID, testID, 0, 1); return err }},
		{"list cursor", contract.TestList{Items: []contract.TestRecord{record}, NextAfter: &other}, func(c *Client) error {
			_, err := c.ListTests(context.Background(), leaseID, contract.ListTestsParams{})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					w.WriteHeader(http.StatusAccepted)
				}
				_ = json.NewEncoder(w).Encode(tc.body)
			}))
			defer server.Close()
			client := &Client{endpoint: server.URL, secret: "secret", http: server.Client()}
			var failure *RequestError
			if err := tc.call(client); !errors.As(err, &failure) || failure.Code != "invalid_response" || (tc.name == "command test" && !failure.OutcomeUnknown) {
				t.Fatalf("unexpected result: %v", err)
			}
		})
	}
}

func TestNativeClientReadsHistoricalDuration(t *testing.T) {
	id := uuid.New()
	record := contract.TestRecord{TestId: id, StopCommandId: uuid.New(), Demo: true, OperatorId: "demo", Parameters: contract.TestParameters{Valve: "V", AreaM2: 1, PmaxPa: 1, MeasurementSeconds: 7200, Direction: "pressure"}, Stage: "finished", Outcome: "interrupted", DataQuality: "incomplete", SaveStatus: "saved", CreatedAt: time.Now().UTC()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(record) }))
	defer server.Close()
	client := &Client{endpoint: server.URL, secret: "secret", http: server.Client()}
	result, err := client.Test(context.Background(), uuid.New(), id)
	if err != nil || result.Parameters.MeasurementSeconds != 7200 {
		t.Fatalf("historical record: %+v %v", result, err)
	}
}

func TestNativeClientRejectsMalformedCommandResponse(t *testing.T) {
	commandID := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"commandId":"` + commandID.String() + `"}`))
	}))
	defer server.Close()
	client := &Client{endpoint: server.URL, secret: "secret", http: server.Client()}
	_, err := client.StartTest(context.Background(), uuid.New(), uuid.New(), contract.StartTestCommand{CommandId: commandID})
	var failure *RequestError
	if !errors.As(err, &failure) || !failure.OutcomeUnknown {
		t.Fatalf("missing required response status: %v", err)
	}
}
