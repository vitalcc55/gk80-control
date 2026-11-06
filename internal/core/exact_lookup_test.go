package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/demo"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

type blockedExactCreate struct {
	database uuid.UUID
	entered  chan struct{}
}

type deadlineRecorder struct{ *httptest.ResponseRecorder }

func (deadlineRecorder) SetReadDeadline(time.Time) error  { return nil }
func (deadlineRecorder) SetWriteDeadline(time.Time) error { return nil }

type deadlineTrackingListener struct {
	net.Listener
	expired chan struct{}
	once    sync.Once
}

func (l *deadlineTrackingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &deadlineTrackingConnection{Conn: conn, listener: l}, nil
}

type deadlineTrackingConnection struct {
	net.Conn
	listener *deadlineTrackingListener
}

func (c *deadlineTrackingConnection) SetWriteDeadline(deadline time.Time) error {
	err := c.Conn.SetWriteDeadline(deadline)
	if !deadline.IsZero() && !deadline.After(time.Now()) {
		c.listener.once.Do(func() { close(c.listener.expired) })
	}
	return err
}

func (b blockedExactCreate) DatabaseID() uuid.UUID { return b.database }
func (b blockedExactCreate) LookupCreateExact(ctx context.Context, _ contract.CreateTestCommand, _ []byte) (contract.CommandStatus, error) {
	close(b.entered)
	<-ctx.Done()
	return contract.CommandStatus{}, ctx.Err()
}

func (blockedExactCreate) LookupStart(context.Context, uuid.UUID, contract.StartTestCommand) (contract.CommandStatus, error) {
	return contract.CommandStatus{}, storage.ErrCommandNotFound
}
func (blockedExactCreate) LookupConfirm(context.Context, uuid.UUID, uuid.UUID, contract.ConfirmActionCommand) (contract.CommandStatus, error) {
	return contract.CommandStatus{}, storage.ErrCommandNotFound
}
func (blockedExactCreate) LookupStopExact(context.Context, uuid.UUID, uuid.UUID) (contract.CommandStatus, error) {
	return contract.CommandStatus{}, storage.ErrCommandNotFound
}

func TestExactCreateLookupMatchesStoredIdentityWithoutExecuting(t *testing.T) {
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
	lease, err := leases.acquire(context.Background(), uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	body := `{"commandId":"` + id.String() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":10,"direction":"pressure","actuator":false}}`
	var command contract.CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", []byte(body), &command)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateExact(context.Background(), command, input.Identity)
	if err != nil || created.TestId == nil {
		t.Fatalf("setup create: %+v %v", created, err)
	}
	beforeAudit, err := store.AuditPage(context.Background(), *created.TestId, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, engine: engine, store: store, exact: store}
	request := func(payload, database, secret string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/commands/lookup/create", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set(leaseHeader, lease.id.String())
		req.Header.Set("X-GK80-Database-Id", database)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(deadlineRecorder{response}, req)
		return response
	}
	response := request(body, store.DatabaseID().String(), "secret")
	var matched contract.CommandStatus
	if err := json.Unmarshal(response.Body.Bytes(), &matched); err != nil || response.Code != http.StatusOK || matched.CommandId != id || matched.TestId == nil || *matched.TestId != *created.TestId || matched.Status != contract.CommandStatusStatusCompleted {
		t.Fatalf("match: %d %s %v", response.Code, response.Body.String(), err)
	}
	equivalent := strings.Replace(body, `"areaM2":1`, `"areaM2":1.0`, 1)
	if response := request(equivalent, store.DatabaseID().String(), "secret"); response.Code != http.StatusOK {
		t.Fatalf("equivalent number: %d %s", response.Code, response.Body.String())
	}
	changed := strings.Replace(body, `"areaM2":1`, `"areaM2":1.0000000000000001`, 1)
	if response := request(changed, store.DatabaseID().String(), "secret"); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"command_conflict"`) {
		t.Fatalf("exact numeric conflict: %d %s", response.Code, response.Body.String())
	}
	missing := strings.Replace(body, id.String(), uuid.NewString(), 1)
	var missingCommand contract.CreateTestCommand
	if err := json.Unmarshal([]byte(missing), &missingCommand); err != nil {
		t.Fatal(err)
	}
	response = request(missing, store.DatabaseID().String(), "secret")
	var unknown contract.CommandStatus
	if err := json.Unmarshal(response.Body.Bytes(), &unknown); err != nil || response.Code != http.StatusOK || unknown.CommandId != missingCommand.CommandId || unknown.Status != contract.CommandStatusStatusUnknown {
		t.Fatalf("unknown: %d %s %v", response.Code, response.Body.String(), err)
	}
	rejectedBody := strings.Replace(missing, `"valve":"V"`, `"valve":" "`, 1)
	var rejectedCommand contract.CreateTestCommand
	rejectedInput, err := validator.Inspect("CreateTestCommand", []byte(rejectedBody), &rejectedCommand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateExact(context.Background(), rejectedCommand, rejectedInput.Identity); err == nil {
		t.Fatal("invalid setup command was accepted")
	}
	response = request(rejectedBody, store.DatabaseID().String(), "secret")
	var rejected contract.CommandStatus
	if err := json.Unmarshal(response.Body.Bytes(), &rejected); err != nil || response.Code != http.StatusOK || rejected.CommandId != rejectedCommand.CommandId || rejected.Status != contract.CommandStatusStatusRejected || rejected.Reason == nil || *rejected.Reason != "invalid_command" {
		t.Fatalf("saved rejection: %d %s %v", response.Code, response.Body.String(), err)
	}
	legacy := command
	legacy.CommandId = uuid.New()
	legacy.Parameters.AreaM2 = 0
	if _, err := store.Create(context.Background(), legacy); err == nil {
		t.Fatal("invalid historical command was accepted")
	}
	legacyBody, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	response = request(string(legacyBody), store.DatabaseID().String(), "secret")
	if err := json.Unmarshal(response.Body.Bytes(), &rejected); err != nil || response.Code != http.StatusOK || rejected.CommandId != legacy.CommandId || rejected.Status != contract.CommandStatusStatusRejected {
		t.Fatalf("historical rejection: %d %s %v", response.Code, response.Body.String(), err)
	}
	if response := request(body, uuid.NewString(), "secret"); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"database_mismatch"`) {
		t.Fatalf("wrong database: %d %s", response.Code, response.Body.String())
	}
	if response := request(body, strings.ReplaceAll(store.DatabaseID().String(), "-", ""), "secret"); response.Code != http.StatusBadRequest {
		t.Fatalf("noncanonical database ID: %d %s", response.Code, response.Body.String())
	}
	if response := request(body, store.DatabaseID().String(), "wrong"); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized: %d", response.Code)
	}
	if response := request(`{"commandId":"`+id.String()+`"}`, store.DatabaseID().String(), "secret"); response.Code != http.StatusBadRequest {
		t.Fatalf("missing fields: %d %s", response.Code, response.Body.String())
	}
	if state := engine.State(); state.Record != nil {
		t.Fatalf("lookup changed engine state: %+v", state.Record)
	}
	afterAudit, err := store.AuditPage(context.Background(), *created.TestId, 0, 100)
	if err != nil || !reflect.DeepEqual(beforeAudit, afterAudit) {
		t.Fatalf("lookup changed audit: %+v %v", afterAudit, err)
	}
	list, _, _, err := store.ListTests(context.Background(), contract.ListTestsParams{After: nil, Limit: new(100)})
	if err != nil || len(list) != 1 || list[0].TestId != *created.TestId {
		t.Fatalf("stored tests changed: %+v %v", list, err)
	}
}

func TestExactCreateLookupStopsWhenLeaseIsReleased(t *testing.T) {
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
	lease, err := leases.acquire(context.Background(), uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	reader := blockedExactCreate{database: store.DatabaseID(), entered: make(chan struct{})}
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, exact: reader}
	body := `{"commandId":"` + uuid.NewString() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":10,"direction":"pressure","actuator":false}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/commands/lookup/create", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set(leaseHeader, lease.id.String())
	req.Header.Set("X-GK80-Database-Id", reader.database.String())
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { handler.ServeHTTP(deadlineRecorder{response}, req); close(done) }()
	select {
	case <-reader.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("lookup did not start")
	}
	if err := leases.release(context.Background(), lease.id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("revoked lookup did not return")
	}
	if response.Code != http.StatusForbidden {
		t.Fatalf("revoked lookup: %d %s", response.Code, response.Body.String())
	}
	select {
	case <-handler.stopWorkers():
	case <-time.After(3 * time.Second):
		t.Fatal("lookup worker did not exit")
	}
}

func TestExactCreateLookupCancelsIncompleteBodyOnLeaseRelease(t *testing.T) {
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
	lease, err := leases.acquire(context.Background(), uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, exact: store}
	server := httptest.NewServer(handler)
	defer server.Close()
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := fmt.Fprintf(conn, "POST /v1/commands/lookup/create HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer secret\r\n%s: %s\r\nX-GK80-Database-Id: %s\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{", server.Listener.Addr(), leaseHeader, lease.id, store.DatabaseID()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		handler.workerMu.Lock()
		workers := handler.workers
		handler.workerMu.Unlock()
		if workers == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("incomplete body did not occupy a bounded worker")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := leases.release(context.Background(), lease.id); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("incomplete body did not finish after revocation: %v", err)
	}
	select {
	case <-handler.stopWorkers():
	case <-time.After(3 * time.Second):
		t.Fatal("body reader did not release its worker")
	}
}

func TestExactCreateLookupKeepsWorkerThroughResponseFlush(t *testing.T) {
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
	lease, err := leases.acquire(context.Background(), uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, exact: store}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gate := &gatedListener{Listener: listener, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(gate.release) })
	server := &http.Server{Handler: handler, WriteTimeout: 5 * time.Second}
	defer func() { _ = server.Close() }()
	go func() { _ = server.Serve(gate) }()
	body := `{"commandId":"` + uuid.NewString() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":10,"direction":"pressure","actuator":false}}`
	req, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/v1/commands/lookup/create", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set(leaseHeader, lease.id.String())
	req.Header.Set("X-GK80-Database-Id", store.DatabaseID().String())
	req.Header.Set("Content-Type", "application/json")
	done := make(chan error, 1)
	go func() {
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err == nil {
			err = response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-gate.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("response write did not start")
	}
	workers := handler.stopWorkers()
	select {
	case <-workers:
		t.Fatal("response flush was not tracked")
	default:
	}
	release.Do(func() { close(gate.release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client did not finish")
	}
	select {
	case <-workers:
	case <-time.After(3 * time.Second):
		t.Fatal("response worker did not finish")
	}
}

func TestExactCreateLookupRevocationInterruptsResponseFlush(t *testing.T) {
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
	lease, err := leases.acquire(context.Background(), uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, exact: store}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gate := &gatedListener{Listener: listener, entered: make(chan struct{}), release: make(chan struct{})}
	tracked := &deadlineTrackingListener{Listener: gate, expired: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(gate.release) })
	server := &http.Server{Handler: handler, WriteTimeout: 5 * time.Second}
	defer func() { _ = server.Close() }()
	go func() { _ = server.Serve(tracked) }()
	body := `{"commandId":"` + uuid.NewString() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":10,"direction":"pressure","actuator":false}}`
	req, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/v1/commands/lookup/create", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set(leaseHeader, lease.id.String())
	req.Header.Set("X-GK80-Database-Id", store.DatabaseID().String())
	req.Header.Set("Content-Type", "application/json")
	type result struct {
		status int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			done <- result{err: err}
			return
		}
		_ = response.Body.Close()
		done <- result{status: response.StatusCode}
	}()
	select {
	case <-gate.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("response write did not start")
	}
	if err := leases.release(context.Background(), lease.id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tracked.expired:
	case <-time.After(3 * time.Second):
		t.Fatal("revocation did not expire write deadline")
	}
	release.Do(func() { close(gate.release) })
	select {
	case result := <-done:
		if result.err == nil && result.status >= 200 && result.status < 300 {
			t.Fatalf("success delivered after revocation: %d", result.status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client did not finish after revocation")
	}
	select {
	case <-handler.stopWorkers():
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not finish after revocation")
	}
}
