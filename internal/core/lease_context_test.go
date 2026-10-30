package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/demo"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

type observedLeaseContext struct {
	context.Context
	once    sync.Once
	checked chan struct{}
	target  int32
	calls   atomic.Int32
}

func TestLeaseCancellationAfterAdmissionDoesNotUndoResult(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine, err := demo.NewEngine(context.Background(), store, demo.NewSimulator(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	handler := &coreHTTP{leases: leases, validator: validator}
	clientID := uuid.New()
	request := func(method, body string, leaseID uuid.UUID) (*blockedResponse, context.CancelFunc, <-chan struct{}) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(method, "/v1/ui-lease", strings.NewReader(body)).WithContext(ctx)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if leaseID != uuid.Nil {
			req.Header.Set(leaseHeader, leaseID.String())
		}
		response := &blockedResponse{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
		done := make(chan struct{})
		go func() { handler.lease(response, req); close(done) }()
		select {
		case <-response.entered:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("response did not reach admitted state")
		}
		return response, cancel, done
	}
	response, cancel, done := request(http.MethodPost, fmt.Sprintf(`{"clientId":%q}`, clientID), uuid.Nil)
	leases.mu.Lock()
	lease := leases.current
	leases.mu.Unlock()
	if lease == nil {
		t.Fatal("acquire not admitted")
	}
	cancel()
	close(response.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("acquire did not finish")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("acquire response: %d", response.Code)
	}
	leases.mu.Lock()
	before := lease.expiresAt
	leases.duration = 2 * time.Minute
	leases.mu.Unlock()
	response, cancel, done = request(http.MethodPut, "", lease.id)
	leases.mu.Lock()
	renewed := lease.expiresAt
	leases.mu.Unlock()
	if !renewed.After(before) {
		t.Fatal("renew not admitted")
	}
	cancel()
	close(response.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("renew did not finish")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("renew response: %d", response.Code)
	}
	leases.mu.Lock()
	valid := leases.current == lease && lease.expiresAt.Equal(renewed)
	leases.mu.Unlock()
	if !valid {
		t.Fatal("admitted lease was rolled back")
	}
	cancelled, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	req := httptest.NewRequest(http.MethodPut, "/v1/ui-lease", nil).WithContext(cancelled)
	req.Header.Set(leaseHeader, lease.id.String())
	recorder := httptest.NewRecorder()
	handler.lease(recorder, req)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), `"code":"state_unavailable"`) {
		t.Fatalf("cancelled response: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestHTTPLeaseCancellationBeforeAdmission(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine, err := demo.NewEngine(context.Background(), store, demo.NewSimulator(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	handler := &coreHTTP{secret: "secret", leases: leases, validator: validator}
	checked := make(chan struct{})
	serverContext := make(chan context.Context, 1)
	served := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverContext <- r.Context()
		observed := &observedLeaseContext{Context: r.Context(), checked: checked}
		handler.ServeHTTP(w, r.WithContext(observed))
		close(served)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/ui-lease", strings.NewReader(fmt.Sprintf(`{"clientId":%q}`, uuid.NewString())))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	leases.mu.Lock()
	done := make(chan error, 1)
	go func() {
		response, err := server.Client().Do(req)
		if response != nil {
			_ = response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-checked:
	case <-time.After(5 * time.Second):
		leases.mu.Unlock()
		t.Fatal("request did not reach admission")
	}
	serverCtx := <-serverContext
	cancel()
	select {
	case <-serverCtx.Done():
	case <-time.After(5 * time.Second):
		leases.mu.Unlock()
		t.Fatal("server did not observe cancellation")
	}
	leases.mu.Unlock()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled request succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not finish")
	}
	leases.mu.Lock()
	defer leases.mu.Unlock()
	if leases.current != nil {
		t.Fatal("cancelled HTTP request granted lease")
	}
}

func (c *observedLeaseContext) Err() error {
	err := c.Context.Err()
	count := c.calls.Add(1)
	if c.target == 0 || count == c.target {
		c.once.Do(func() { close(c.checked) })
	}
	return err
}

func TestHTTPCancelledRenewWhileEngineBusyKeepsActiveTest(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	port := &gatedReadiness{Equipment: demo.NewSimulator(), entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(port.release) }) }
	defer releaseGate()
	engine, err := demo.NewEngine(context.Background(), store, port, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	lease, err := leases.acquire(context.Background(), uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := lease.controller.Create(context.Background(), contract.CreateTestCommand{CommandId: uuid.New(), Parameters: contract.TestParameters{Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 60, Direction: "pressure"}})
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %+v %v", created, err)
	}
	id := *created.TestId
	if _, err := lease.controller.Start(context.Background(), id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	state := engine.State()
	if state.PendingAction == nil {
		t.Fatal("missing confirmation")
	}
	if _, err := lease.controller.Confirm(context.Background(), id, state.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: state.Revision}); err != nil {
		t.Fatal(err)
	}
	before := lease.expiresAt
	port.block.Store(true)
	readinessDone := make(chan struct{})
	go func() { _, _ = engine.Readiness(id); close(readinessDone) }()
	select {
	case <-port.entered:
	case <-time.After(5 * time.Second):
		releaseGate()
		t.Fatal("engine did not reach gate")
	}
	checked, served := make(chan struct{}), make(chan struct{})
	serverContext := make(chan context.Context, 1)
	handler := &coreHTTP{secret: "secret", leases: leases}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverContext <- r.Context()
		observed := &observedLeaseContext{Context: r.Context(), checked: checked, target: 3}
		handler.ServeHTTP(w, r.WithContext(observed))
		close(served)
	}))
	defer func() { releaseGate(); server.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, server.URL+"/v1/ui-lease", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set(leaseHeader, lease.id.String())
	done := make(chan error, 1)
	go func() {
		response, err := server.Client().Do(req)
		if response != nil {
			_ = response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-checked:
	case <-time.After(5 * time.Second):
		releaseGate()
		t.Fatal("renew did not reach engine")
	}
	serverCtx := <-serverContext
	cancel()
	select {
	case <-serverCtx.Done():
	case <-time.After(5 * time.Second):
		releaseGate()
		t.Fatal("server did not observe cancellation")
	}
	releaseGate()
	select {
	case <-readinessDone:
	case <-time.After(5 * time.Second):
		t.Fatal("readiness did not finish")
	}
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("renew did not finish")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled request succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not finish")
	}
	leases.mu.Lock()
	unchanged := leases.current == lease && lease.expiresAt.Equal(before)
	leases.mu.Unlock()
	if !unchanged {
		t.Fatal("cancelled renew changed lease")
	}
	select {
	case <-lease.done:
		t.Fatal("cancelled renew revoked controller")
	default:
	}
	state = engine.State()
	if state.Record == nil || state.Record.Stage != "running" || state.Stop != nil {
		t.Fatalf("active test stopped: %+v", state.Record)
	}
	if _, err := lease.controller.Stop(context.Background(), id, state.Record.StopCommandId); err != nil {
		t.Fatalf("stop: %v", err)
	}
	shutdown, finish := context.WithTimeout(context.Background(), 5*time.Second)
	defer finish()
	if err := engine.Shutdown(shutdown, "owner_lost"); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledLeaseAdmissionDoesNotMutateOrStop(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine, err := demo.NewEngine(context.Background(), store, demo.NewSimulator(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	ctx, cancel := context.WithCancel(context.Background())
	observed := &observedLeaseContext{Context: ctx, checked: make(chan struct{})}
	leases.mu.Lock()
	done := make(chan error, 1)
	go func() { _, err := leases.acquire(observed, uuid.New(), uuid.Nil); done <- err }()
	select {
	case <-observed.checked:
	case <-time.After(5 * time.Second):
		leases.mu.Unlock()
		t.Fatal("acquire did not check context")
	}
	cancel()
	leases.mu.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquire: %v", err)
	}
	leases.mu.Lock()
	if leases.current != nil {
		leases.mu.Unlock()
		t.Fatal("cancelled acquire granted lease")
	}
	leases.mu.Unlock()
	lease, err := leases.acquire(context.Background(), uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := lease.controller.Create(context.Background(), contract.CreateTestCommand{CommandId: uuid.New(), Parameters: contract.TestParameters{Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 10, Direction: "pressure"}})
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %+v %v", created, err)
	}
	id := *created.TestId
	if _, err := lease.controller.Start(context.Background(), id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	state := engine.State()
	if _, err := lease.controller.Confirm(context.Background(), id, state.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: state.Revision}); err != nil {
		t.Fatal(err)
	}
	before := lease.expiresAt
	ctx, cancel = context.WithCancel(context.Background())
	observed = &observedLeaseContext{Context: ctx, checked: make(chan struct{})}
	leases.mu.Lock()
	go func() { _, err := leases.renew(observed, lease.id); done <- err }()
	select {
	case <-observed.checked:
	case <-time.After(5 * time.Second):
		leases.mu.Unlock()
		t.Fatal("renew did not check context")
	}
	cancel()
	leases.mu.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled renew: %v", err)
	}
	leases.mu.Lock()
	unchanged := leases.current == lease && lease.expiresAt.Equal(before)
	leases.mu.Unlock()
	if !unchanged {
		t.Fatal("cancelled renew changed lease")
	}
	select {
	case <-lease.done:
		t.Fatal("cancelled renew revoked control")
	default:
	}
	state = engine.State()
	if state.Record == nil || state.Record.Stage != "running" || state.Stop != nil {
		t.Fatalf("active test was stopped: %+v", state.Record)
	}
	stopID := state.Record.StopCommandId
	if _, err := lease.controller.Stop(context.Background(), id, stopID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := engine.Shutdown(shutdown, "owner_lost"); err != nil {
		t.Fatal(err)
	}
}
