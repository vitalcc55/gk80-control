package core

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
	"github.com/vitalcc55/gk80-control/internal/demo"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

type gatedReadiness struct {
	demo.Equipment
	block   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

type countedEquipment struct {
	demo.Equipment
	stops  atomic.Int32
	starts atomic.Int32
}

func (s *countedEquipment) Start(pressure float64) error {
	s.starts.Add(1)
	return s.Equipment.Start(pressure)
}

func (s *countedEquipment) Stop() error {
	s.stops.Add(1)
	return s.Equipment.Stop()
}

func TestLeaseExpiryAndShutdownStopSimulatorOnce(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	port := &countedEquipment{Equipment: demo.NewSimulator()}
	engine, err := demo.NewEngine(context.Background(), store, port, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	lease, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := lease.controller.Create(context.Background(), contract.CreateTestCommand{CommandId: uuid.New(), Parameters: contract.TestParameters{Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 10, Direction: "pressure"}})
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	if _, err := lease.controller.Start(context.Background(), id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	state := engine.State()
	if state.PendingAction == nil {
		t.Fatal("missing action")
	}
	if _, err := lease.controller.Confirm(context.Background(), id, state.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: state.Revision}); err != nil {
		t.Fatal(err)
	}
	if port.starts.Load() != 1 {
		t.Fatalf("simulator did not start: %d", port.starts.Load())
	}
	measurementDeadline := time.After(5 * time.Second)
	for len(engine.State().Measurements) == 0 {
		select {
		case <-engine.Changes():
		case <-measurementDeadline:
			t.Fatal("simulator produced no measurements")
		}
	}
	leases.mu.Lock()
	leases.duration = 50 * time.Millisecond
	leases.mu.Unlock()
	if _, err := leases.renew(lease.id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lease.done:
	case <-time.After(5 * time.Second):
		t.Fatal("lease did not expire")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatal(err)
	}
	if port.stops.Load() != 1 {
		t.Fatalf("stop calls: %d", port.stops.Load())
	}
	audit, err := store.Audit(context.Background(), id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range audit {
		if event.Action == "stop" && event.Outcome == "accepted" && event.Reason != nil && *event.Reason == "ui_lost" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing expiry audit: %+v", audit)
	}
}

func (s *gatedReadiness) Ready() bool {
	if s.block.Swap(false) {
		close(s.entered)
		<-s.release
	}
	return s.Equipment.Ready()
}

func TestReadinessRejectsConcurrentStart(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	port := &gatedReadiness{Equipment: demo.NewSimulator(), entered: make(chan struct{}), release: make(chan struct{})}
	engine, err := demo.NewEngine(context.Background(), store, port, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	lease, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := lease.controller.Create(context.Background(), contract.CreateTestCommand{CommandId: uuid.New(), Parameters: contract.TestParameters{Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 10, Direction: "pressure"}})
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, feed: newEventFeed(engine), engine: engine, store: store}
	request := func(method, path, body string) *http.Request {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set(leaseHeader, lease.id.String())
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		return req
	}
	port.block.Store(true)
	readinessDone := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request(http.MethodGet, "/v1/tests/"+id.String()+"/readiness", ""))
		readinessDone <- response.Code
	}()
	select {
	case <-port.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("readiness did not start")
	}
	startDone := make(chan int, 1)
	body := `{"commandId":"` + uuid.NewString() + `","expectedRevision":1,"valveClosed":true,"connectionConfirmed":true}`
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request(http.MethodPost, "/v1/tests/"+id.String()+"/start", body))
		startDone <- response.Code
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		handler.pendingMu.Lock()
		pending := len(handler.pending) != 0
		handler.pendingMu.Unlock()
		if pending {
			break
		}
		if time.Now().After(deadline) {
			close(port.release)
			t.Fatal("start did not register")
		}
		time.Sleep(time.Millisecond)
	}
	close(port.release)
	if code := <-readinessDone; code != http.StatusServiceUnavailable {
		t.Fatalf("unstable readiness: %d", code)
	}
	if code := <-startDone; code != http.StatusAccepted {
		t.Fatalf("start: %d", code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatal(err)
	}
}

func TestStartReadsHistoricalInvalidRejectionWithoutWritingNewOne(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	created, err := store.Create(context.Background(), contract.CreateTestCommand{CommandId: uuid.New(), Parameters: contract.TestParameters{Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 10, Direction: "pressure"}})
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	rejected := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: false, ConnectionConfirmed: true}
	action := contract.PendingAction{ActionId: uuid.New(), TestId: id, Instruction: "Подтверждение"}
	if _, err := store.Start(context.Background(), id, rejected, action); !errors.Is(err, storage.ErrInvalidCommand) {
		t.Fatalf("seed: %v", err)
	}
	engine, err := demo.NewEngine(context.Background(), store, demo.NewSimulator(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	lease, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, feed: newEventFeed(engine), engine: engine, store: store}
	body, err := json.Marshal(rejected)
	if err != nil {
		t.Fatal(err)
	}
	send := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/tests/"+id.String()+"/start", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set(leaseHeader, lease.id.String())
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	response := send(string(body))
	var status contract.CommandStatus
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &status) != nil || status.Status != "rejected" {
		t.Fatalf("legacy rejection: %d %s", response.Code, response.Body.String())
	}
	unknownID := uuid.New()
	response = send(strings.Replace(string(body), rejected.CommandId.String(), unknownID.String(), 1))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("new invalid: %d", response.Code)
	}
	if _, err := store.Command(context.Background(), unknownID); !errors.Is(err, storage.ErrCommandNotFound) {
		t.Fatalf("new invalid was saved: %v", err)
	}
	response = send(strings.Replace(string(body), `"valveClosed":false`, `"valveClosed":true`, 1))
	if response.Code != http.StatusConflict {
		t.Fatalf("conflicting replay: %d", response.Code)
	}
}

type heldStartCommit struct {
	*storage.Store
	entered chan struct{}
	release chan struct{}
}

func (s *heldStartCommit) Start(ctx context.Context, id uuid.UUID, cmd contract.StartTestCommand, action contract.PendingAction) (contract.CommandStatus, error) {
	result, err := s.Store.Start(ctx, id, cmd, action)
	close(s.entered)
	<-s.release
	return result, err
}

func TestStartUnknownResponseThenConfirmedSnapshot(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	held := &heldStartCommit{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	engine, err := demo.NewEngine(context.Background(), held, demo.NewSimulator(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	lease, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := lease.controller.Create(context.Background(), contract.CreateTestCommand{CommandId: uuid.New(), Parameters: contract.TestParameters{Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 10, Direction: "pressure"}})
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	commandID := uuid.New()
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, feed: newEventFeed(engine), engine: engine, store: store}
	request := func(method, path, body string) *http.Request {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set(leaseHeader, lease.id.String())
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		return req
	}
	body := `{"commandId":"` + commandID.String() + `","expectedRevision":1,"valveClosed":true,"connectionConfirmed":true}`
	done := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request(http.MethodPost, "/v1/tests/"+id.String()+"/start", body))
		done <- response.Code
	}()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("start did not commit")
	}
	select {
	case code := <-done:
		if code != http.StatusServiceUnavailable {
			t.Fatalf("unknown response: %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP did not time out")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request(http.MethodGet, "/v1/commands/"+commandID.String(), ""))
	var status contract.CommandStatus
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &status) != nil || status.Status != "accepted" {
		t.Fatalf("status: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request(http.MethodGet, "/v1/snapshot", ""))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("stale snapshot: %d", response.Code)
	}
	close(held.release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request(http.MethodGet, "/v1/snapshot", ""))
		var snapshot contract.Snapshot
		if response.Code == http.StatusOK && json.Unmarshal(response.Body.Bytes(), &snapshot) == nil && snapshot.PendingAction != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("snapshot did not recover")
		}
		time.Sleep(5 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatal(err)
	}
}

type heldConfirmCommit struct {
	*storage.Store
	entered chan struct{}
	release chan struct{}
}

func (s *heldConfirmCommit) Confirm(ctx context.Context, id, action uuid.UUID, cmd contract.ConfirmActionCommand) (contract.CommandStatus, error) {
	result, err := s.Store.Confirm(ctx, id, action, cmd)
	close(s.entered)
	<-s.release
	return result, err
}

func TestRevokedConfirmDoesNotStartSimulatorAfterCommit(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	held := &heldConfirmCommit{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	port := &countedEquipment{Equipment: demo.NewSimulator()}
	engine, err := demo.NewEngine(context.Background(), held, port, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	lease, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := lease.controller.Create(context.Background(), contract.CreateTestCommand{CommandId: uuid.New(), Parameters: contract.TestParameters{Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 10, Direction: "pressure"}})
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	if _, err := lease.controller.Start(context.Background(), id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	state := engine.State()
	if state.PendingAction == nil {
		t.Fatal("missing action")
	}
	action := state.PendingAction.ActionId
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, feed: newEventFeed(engine), engine: engine, store: store}
	body := `{"commandId":"` + uuid.NewString() + `","expectedRevision":2}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tests/"+id.String()+"/actions/"+action.String()+"/confirm", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set(leaseHeader, lease.id.String())
	req.Header.Set("Content-Type", "application/json")
	done := make(chan int, 1)
	go func() { response := httptest.NewRecorder(); handler.ServeHTTP(response, req); done <- response.Code }()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("confirm did not commit")
	}
	if err := leases.release(lease.id); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != http.StatusForbidden {
			t.Fatalf("revoked response: %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revoked HTTP did not return")
	}
	close(held.release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatal(err)
	}
	if port.starts.Load() != 0 || port.stops.Load() != 1 {
		t.Fatalf("simulator starts=%d stops=%d", port.starts.Load(), port.stops.Load())
	}
}

func TestActiveStopBypassesSaturatedWorkers(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine, err := demo.NewEngine(context.Background(), store, demo.NewSimulator(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	lease, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := lease.controller.Create(context.Background(), contract.CreateTestCommand{CommandId: uuid.New(), Parameters: contract.TestParameters{Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 10, Direction: "pressure"}})
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	if _, err := lease.controller.Start(context.Background(), id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	state := engine.State()
	if state.PendingAction == nil {
		t.Fatal("missing action")
	}
	if _, err := lease.controller.Confirm(context.Background(), id, state.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: state.Revision}); err != nil {
		t.Fatal(err)
	}
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, feed: newEventFeed(engine), engine: engine, store: store}
	for range 32 {
		if !handler.beginWorker() {
			t.Fatal("worker was not admitted")
		}
	}
	defer func() {
		for range 32 {
			handler.endWorker()
		}
	}()
	stopID := engine.State().Record.StopCommandId
	req := httptest.NewRequest(http.MethodPost, "/v1/tests/"+id.String()+"/stop", strings.NewReader(`{"commandId":"`+stopID.String()+`"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set(leaseHeader, lease.id.String())
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	var accepted contract.CommandAccepted
	if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &accepted) != nil || accepted.Status != "accepted" {
		t.Fatalf("stop: %d %s", response.Code, response.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatal(err)
	}
	if state := engine.State(); state.Stop == nil || state.Stop.Status != "completed" || state.Record.SaveStatus != "saved" {
		t.Fatalf("stop not confirmed: %+v", state)
	}
}

type failedStopLookup struct{ *storage.Store }

func (*failedStopLookup) LookupStop(context.Context, uuid.UUID, uuid.UUID) (contract.CommandStatus, error) {
	return contract.CommandStatus{}, storage.ErrStorageUnavailable
}

func TestStopLookupStorageFailureInterruptsActiveRun(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine, err := demo.NewEngine(context.Background(), &failedStopLookup{Store: store}, demo.NewSimulator(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	lease, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := lease.controller.Create(context.Background(), contract.CreateTestCommand{CommandId: uuid.New(), Parameters: contract.TestParameters{Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 10, Direction: "pressure"}})
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	id := *created.TestId
	if _, err := lease.controller.Start(context.Background(), id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	state := engine.State()
	if state.PendingAction == nil {
		t.Fatal("missing action")
	}
	if _, err := lease.controller.Confirm(context.Background(), id, state.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: state.Revision}); err != nil {
		t.Fatal(err)
	}
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, feed: newEventFeed(engine), engine: engine, store: store}
	req := httptest.NewRequest(http.MethodPost, "/v1/tests/"+id.String()+"/stop", strings.NewReader(`{"commandId":"`+uuid.NewString()+`"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set(leaseHeader, lease.id.String())
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("stop lookup: %d %s", response.Code, response.Body.String())
	}
	deadline := time.After(5 * time.Second)
	for {
		state := engine.State()
		if state.Stop != nil && state.Stop.Status == "completed" && state.Record.SaveStatus == "saved" {
			break
		}
		select {
		case <-engine.Changes():
		case <-deadline:
			t.Fatal("storage failure did not stop run")
		}
	}
	audit, err := store.Audit(context.Background(), id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range audit {
		if event.Action == "stop" && event.Outcome == "accepted" && event.Reason != nil && *event.Reason == "storage_error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing stop audit: %+v", audit)
	}
}
