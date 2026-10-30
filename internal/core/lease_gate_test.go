package core

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/demo"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

func TestLeaseMutationGateBoundsRequestsWithoutBlockingStop(t *testing.T) {
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
	handler := &coreHTTP{secret: "secret", leases: leases, validator: validator, engine: engine, store: store, feed: newEventFeed(engine)}
	response := &blockedResponse{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(response.release) }) }
	defer release()
	req := httptest.NewRequest(http.MethodPost, "/v1/ui-lease", strings.NewReader(fmt.Sprintf(`{"clientId":%q}`, uuid.NewString())))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, req); close(done) }()
	select {
	case <-response.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("lease response did not block")
	}
	leases.mu.Lock()
	lease := leases.current
	leases.mu.Unlock()
	if lease == nil {
		t.Fatal("lease not granted")
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
		t.Fatal("missing action")
	}
	if _, err := lease.controller.Confirm(context.Background(), id, state.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: state.Revision}); err != nil {
		t.Fatal(err)
	}
	delayed := &delayedBody{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(fmt.Sprintf(`{"clientId":%q}`, uuid.NewString()))}
	var bodyReleaseOnce sync.Once
	releaseBody := func() { bodyReleaseOnce.Do(func() { close(delayed.release) }) }
	defer releaseBody()
	slow := httptest.NewRequest(http.MethodPost, "/v1/ui-lease", nil)
	slow.Header.Set("Authorization", "Bearer secret")
	slow.Header.Set("Content-Type", "application/json")
	slow.Body = delayed
	slowResponse := httptest.NewRecorder()
	slowDone := make(chan struct{})
	go func() { handler.ServeHTTP(slowResponse, slow); close(slowDone) }()
	select {
	case <-delayed.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("second client did not begin sending the lease request")
	}
	extra := httptest.NewRequest(http.MethodPost, "/v1/ui-lease", strings.NewReader(fmt.Sprintf(`{"clientId":%q}`, uuid.NewString())))
	extra.Header.Set("Authorization", "Bearer secret")
	extra.Header.Set("Content-Type", "application/json")
	extraResponse := httptest.NewRecorder()
	handler.ServeHTTP(extraResponse, extra)
	if extraResponse.Code != http.StatusConflict {
		t.Fatalf("additional body reader was admitted: %d", extraResponse.Code)
	}
	renew := httptest.NewRequest(http.MethodPut, "/v1/ui-lease", nil)
	renew.Header.Set("Authorization", "Bearer secret")
	renew.Header.Set(leaseHeader, lease.id.String())
	renewed := httptest.NewRecorder()
	handler.ServeHTTP(renewed, renew)
	state = engine.State()
	if renewed.Code != http.StatusOK || state.Record == nil || state.Record.Stage != "running" || state.Stop != nil || !leases.valid(lease) {
		t.Fatalf("slow second client prevented renewal: %d", renewed.Code)
	}
	releaseBody()
	select {
	case <-slowDone:
	case <-time.After(5 * time.Second):
		t.Fatal("second client did not finish")
	}
	var busy contract.ApiError
	if slowResponse.Code != http.StatusConflict || json.Unmarshal(slowResponse.Body.Bytes(), &busy) != nil || busy.Code != "lease_busy" {
		t.Fatalf("second client: %d %s", slowResponse.Code, slowResponse.Body.String())
	}
	handler.leaseGate.Lock()
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		body := ""
		if method == http.MethodPost {
			body = fmt.Sprintf(`{"clientId":%q}`, uuid.NewString())
		}
		request := httptest.NewRequest(method, "/v1/ui-lease", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer secret")
		request.Header.Set(leaseHeader, lease.id.String())
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		var apiError contract.ApiError
		if recorder.Code != http.StatusConflict || json.Unmarshal(recorder.Body.Bytes(), &apiError) != nil || apiError.Code != "lease_operation_in_progress" {
			handler.leaseGate.Unlock()
			t.Fatalf("%s: %d %s", method, recorder.Code, recorder.Body.String())
		}
	}
	state = engine.State()
	stopID := state.Record.StopCommandId
	stop := httptest.NewRequest(http.MethodPost, "/v1/tests/"+id.String()+"/stop", strings.NewReader(fmt.Sprintf(`{"commandId":%q}`, stopID)))
	stop.Header.Set("Authorization", "Bearer secret")
	stop.Header.Set(leaseHeader, lease.id.String())
	stop.Header.Set("Content-Type", "application/json")
	stopped := httptest.NewRecorder()
	handler.ServeHTTP(stopped, stop)
	handler.leaseGate.Unlock()
	if stopped.Code != http.StatusAccepted {
		t.Fatalf("stop blocked by lease gate: %d %s", stopped.Code, stopped.Body.String())
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("lease handler did not finish")
	}
	renew = httptest.NewRequest(http.MethodPut, "/v1/ui-lease", nil)
	renew.Header.Set("Authorization", "Bearer secret")
	renew.Header.Set(leaseHeader, lease.id.String())
	renewed = httptest.NewRecorder()
	handler.ServeHTTP(renewed, renew)
	if renewed.Code != http.StatusOK {
		t.Fatalf("lease gate did not reopen: %d", renewed.Code)
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(shutdown, "owner_lost"); err != nil {
		t.Fatal(err)
	}
}

type signaledBody struct {
	io.ReadCloser
	once    sync.Once
	entered chan struct{}
}

func (b *signaledBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	return b.ReadCloser.Read(p)
}

func TestLeaseBodyAdmissionRejectsIncompleteRequestOverHTTP(t *testing.T) {
	handler := &coreHTTP{secret: "secret"}
	entered := make(chan struct{})
	firstDone := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-First-Request") == "yes" {
			defer close(firstDone)
			r.Body = &signaledBody{ReadCloser: r.Body, entered: entered}
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	dial := func() (*tls.Conn, error) {
		return tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", server.Listener.Addr().String(), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	}
	first, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	_, err = fmt.Fprint(first, "POST /v1/ui-lease HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer secret\r\nContent-Type: application/json\r\nContent-Length: 100\r\nX-First-Request: yes\r\n\r\n{")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first body did not begin reading")
	}
	second, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	if err := second.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprint(second, "POST /v1/ui-lease HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer secret\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{")
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(second), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatalf("incomplete second body delayed the refusal: %v", err)
	}
	defer func(body io.ReadCloser) { _ = body.Close() }(response.Body)
	var apiError contract.ApiError
	if response.StatusCode != http.StatusConflict || !response.Close || json.NewDecoder(response.Body).Decode(&apiError) != nil || apiError.Code != "lease_operation_in_progress" {
		t.Fatalf("second request: status=%d close=%v error=%+v", response.StatusCode, response.Close, apiError)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	var trailing [1]byte
	if _, err := second.Read(trailing[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("rejected connection was not closed promptly: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not release its body admission")
	}
	oversized, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = oversized.Close() }()
	if err := oversized.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprint(oversized, "POST /v1/ui-lease HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer secret\r\nContent-Type: application/json\r\nContent-Length: 9000\r\n\r\n", strings.Repeat("x", 8193))
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.ReadResponse(bufio.NewReader(oversized), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatalf("oversized body delayed the refusal: %v", err)
	}
	defer func(body io.ReadCloser) { _ = body.Close() }(response.Body)
	if response.StatusCode != http.StatusBadRequest || !response.Close {
		t.Fatalf("oversized request: status=%d close=%v", response.StatusCode, response.Close)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	if _, err := oversized.Read(trailing[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("oversized connection was not closed promptly: %v", err)
	}
}

type blockedLeaseHeader struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release chan struct{}
}

func (w *blockedLeaseHeader) WriteHeader(status int) {
	w.ResponseRecorder.WriteHeader(status)
	close(w.entered)
	<-w.release
}

func TestLeaseReleaseCancellationAfterAdmissionDoesNotUndo(t *testing.T) {
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
	lease, err := leases.acquire(context.Background(), uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := &coreHTTP{leases: leases}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodDelete, "/v1/ui-lease", nil).WithContext(ctx)
	req.Header.Set(leaseHeader, lease.id.String())
	response := &blockedLeaseHeader{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(response.release) }) }
	defer release()
	done := make(chan struct{})
	go func() { handler.lease(response, req); close(done) }()
	select {
	case <-response.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("release not admitted")
	}
	cancel()
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("release did not finish")
	}
	if response.Code != http.StatusNoContent || leases.valid(lease) {
		t.Fatalf("admitted release was undone: %d", response.Code)
	}
}

func TestCancelledLeaseReleaseBeforeAdmissionPreservesOwner(t *testing.T) {
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
	lease, err := leases.acquire(context.Background(), uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	observed := &observedLeaseContext{Context: ctx, checked: make(chan struct{})}
	leases.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- leases.release(observed, lease.id) }()
	select {
	case <-observed.checked:
	case <-time.After(5 * time.Second):
		leases.mu.Unlock()
		t.Fatal("release did not check context")
	}
	cancel()
	leases.mu.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("release: %v", err)
	}
	if !leases.valid(lease) {
		t.Fatal("cancelled release revoked owner")
	}
	handler := &coreHTTP{leases: leases}
	req := httptest.NewRequest(http.MethodDelete, "/v1/ui-lease", nil).WithContext(ctx)
	req.Header.Set(leaseHeader, lease.id.String())
	recorder := httptest.NewRecorder()
	handler.lease(recorder, req)
	if recorder.Code != http.StatusServiceUnavailable || !leases.valid(lease) {
		t.Fatalf("cancelled HTTP release: %d", recorder.Code)
	}
	if err := leases.release(context.Background(), lease.id); err != nil {
		t.Fatal(err)
	}
}
