package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/demo"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

func waitStopLookupWorkers(t *testing.T, handler *coreHTTP) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		handler.workerMu.Lock()
		remaining := handler.stopLookupWorkers
		handler.workerMu.Unlock()
		if remaining == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Stop lookup workers did not return")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestExactStopRouteReadsIdentityWithoutStartingEquipment(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine, err := demo.NewEngine(ctx, store, demo.NewSimulator(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	lease, err := leases.acquire(ctx, uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	create := contract.CreateTestCommand{CommandId: uuid.New(), ExpectedRevision: 0, Parameters: contract.TestParameters{PreOpenStabilizationSeconds: 10, PostCloseStabilizationSeconds: 10, Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 60, Direction: "pressure"}}
	created, err := store.Create(ctx, create)
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %+v %v", created, err)
	}
	record, err := store.Test(ctx, *created.TestId)
	if err != nil {
		t.Fatal(err)
	}
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, engine: engine, exact: store}
	request := func(testID, database uuid.UUID, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/commands/lookup/stop/"+testID.String(), strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set(leaseHeader, lease.id.String())
		req.Header.Set("X-GK80-Database-Id", database.String())
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(deadlineRecorder{response}, req)
		waitStopLookupWorkers(t, handler)
		return response
	}
	body := `{"commandId":"` + record.StopCommandId.String() + `"}`
	for i := 0; i < 32; i++ {
		if !handler.beginWorker() {
			t.Fatalf("ordinary worker %d not admitted", i)
		}
	}
	response := request(record.TestId, store.DatabaseID(), body)
	for i := 0; i < 32; i++ {
		handler.endWorker()
	}
	if response.Code != http.StatusOK {
		t.Fatalf("Stop lookup starved by ordinary workers: %d %s", response.Code, response.Body.String())
	}
	for _, tc := range []struct {
		id   uuid.UUID
		want contract.CommandStatusStatus
	}{{record.StopCommandId, contract.CommandStatusStatusUnknown}, {uuid.New(), contract.CommandStatusStatusUnknown}} {
		response := request(record.TestId, store.DatabaseID(), `{"commandId":"`+tc.id.String()+`"}`)
		var status contract.CommandStatus
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || response.Code != http.StatusOK || status.CommandId != tc.id || status.Status != tc.want {
			t.Fatalf("unknown: %d %s %v", response.Code, response.Body.String(), err)
		}
	}
	for _, tc := range []struct {
		testID  uuid.UUID
		payload string
	}{{record.TestId, `{"commandId":"` + create.CommandId.String() + `"}`}, {uuid.New(), body}} {
		response := request(tc.testID, store.DatabaseID(), tc.payload)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"command_conflict"`) {
			t.Fatalf("conflict: %d %s", response.Code, response.Body.String())
		}
	}
	if response := request(record.TestId, uuid.New(), body); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"database_mismatch"`) {
		t.Fatalf("database: %d %s", response.Code, response.Body.String())
	}
	if response := request(record.TestId, store.DatabaseID(), `{}`); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid body: %d %s", response.Code, response.Body.String())
	}
	if state := engine.State(); state.Record != nil {
		t.Fatalf("lookup started equipment: %+v", state.Record)
	}
	result, err := store.FinishStop(ctx, record.TestId, record.StopCommandId, storage.StopDetails{Actor: "demo-operator", Reason: "operator", AcceptedAt: time.Now()})
	if err != nil || result.Command.Status != contract.CommandStatusStatusCompleted {
		t.Fatalf("finish: %+v %v", result, err)
	}
	before, err := store.AuditPage(ctx, record.TestId, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	response = request(record.TestId, store.DatabaseID(), body)
	var status contract.CommandStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || response.Code != http.StatusOK || status.Status != contract.CommandStatusStatusCompleted || status.TestId == nil || *status.TestId != record.TestId {
		t.Fatalf("completed: %d %s %v", response.Code, response.Body.String(), err)
	}
	after, err := store.AuditPage(ctx, record.TestId, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("lookup changed audit: %v", err)
	}
}

func TestExactStopLookupDoesNotStopActiveEquipment(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	port := &countedEquipment{Equipment: demo.NewSimulator()}
	engine, err := demo.NewEngine(ctx, store, port, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	lease, err := leases.acquire(ctx, uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := engine.Shutdown(shutdownCtx, "owner_lost"); err != nil {
			t.Errorf("shutdown engine: %v", err)
		}
	}()
	created, err := lease.controller.Create(ctx, contract.CreateTestCommand{CommandId: uuid.New(), Parameters: contract.TestParameters{PreOpenStabilizationSeconds: 10, PostCloseStabilizationSeconds: 10, Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 60, Direction: "pressure"}})
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %+v %v", created, err)
	}
	if _, err := lease.controller.Start(ctx, *created.TestId, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	pending := engine.State()
	if pending.Record == nil || pending.PendingAction == nil {
		t.Fatal("active test did not start")
	}
	if _, err := lease.controller.Confirm(ctx, *created.TestId, pending.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: pending.Revision}); err != nil {
		t.Fatal(err)
	}
	before := engine.State()
	if port.starts.Load() != 1 {
		t.Fatal("simulator did not start")
	}
	audit, err := store.AuditPage(ctx, *created.TestId, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, engine: engine, exact: store}
	body := `{"commandId":"` + before.Record.StopCommandId.String() + `"}`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/commands/lookup/stop/"+created.TestId.String(), strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set(leaseHeader, lease.id.String())
		req.Header.Set("X-GK80-Database-Id", store.DatabaseID().String())
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(deadlineRecorder{response}, req)
		waitStopLookupWorkers(t, handler)
		var status contract.CommandStatus
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || response.Code != http.StatusOK || status.Status != contract.CommandStatusStatusUnknown {
			t.Fatalf("reserved: %d %s %v", response.Code, response.Body.String(), err)
		}
	}
	after := engine.State()
	afterAudit, err := store.AuditPage(ctx, *created.TestId, 0, 100)
	if err != nil || !reflect.DeepEqual(audit, afterAudit) || after.Record == nil || before.Record.TestId != after.Record.TestId || before.Record.StopCommandId != after.Record.StopCommandId || before.Record.Stage != after.Record.Stage || before.Record.Outcome != after.Record.Outcome || port.stops.Load() != 0 {
		t.Fatalf("lookup changed active run: stops=%d err=%v", port.stops.Load(), err)
	}
}

func TestStopLookupWorkerIsBoundedAndDrained(t *testing.T) {
	h := &coreHTTP{}
	for i := 0; i < 2; i++ {
		if !h.beginStopLookupWorker() {
			t.Fatal("Stop lookup not admitted")
		}
	}
	if h.beginStopLookupWorker() {
		t.Fatal("Stop lookup worker limit exceeded")
	}
	for i := 0; i < 32; i++ {
		if !h.beginWorker() {
			t.Fatalf("ordinary worker %d starved by Stop lookup", i)
		}
	}
	if h.beginWorker() {
		t.Fatal("ordinary worker limit exceeded")
	}
	done := h.stopWorkers()
	if h.beginStopLookupWorker() || h.beginWorker() {
		t.Fatal("worker admitted during shutdown")
	}
	h.endStopLookupWorker()
	for i := 0; i < 32; i++ {
		h.endWorker()
	}
	select {
	case <-done:
		t.Fatal("shutdown completed before all workers")
	default:
	}
	h.endStopLookupWorker()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("workers did not drain")
	}
}
