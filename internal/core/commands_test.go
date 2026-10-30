package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/demo"
	"github.com/vitalcc55/gk80-control/internal/diagnostics"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

type lostCreateCommit struct{ *storage.Store }

type delayedBody struct {
	entered chan struct{}
	release chan struct{}
	reader  io.Reader
	once    sync.Once
}

func (b *delayedBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.reader.Read(p)
}

func (*delayedBody) Close() error { return nil }

func TestRevokedOwnerCannotRegisterCreateAfterDelayedBody(t *testing.T) {
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
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, feed: newEventFeed(engine), engine: engine, store: store}
	body := `{"commandId":"` + uuid.NewString() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	delayed := &delayedBody{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(body)}
	req := httptest.NewRequest(http.MethodPost, "/v1/tests", nil)
	req.Body = delayed
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set(leaseHeader, lease.id.String())
	req.Header.Set("Content-Type", "application/json")
	done := make(chan int, 1)
	go func() { response := httptest.NewRecorder(); handler.ServeHTTP(response, req); done <- response.Code }()
	select {
	case <-delayed.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("body did not start")
	}
	if err := leases.release(lease.id); err != nil {
		t.Fatal(err)
	}
	if _, err := leases.acquire(uuid.New(), uuid.Nil); err != nil {
		t.Fatal(err)
	}
	close(delayed.release)
	if code := <-done; code != http.StatusForbidden {
		t.Fatalf("revoked request: %d", code)
	}
	if handler.anyAttemptInFlight() {
		t.Fatal("revoked request was admitted")
	}
}

func (s *lostCreateCommit) CreateExact(ctx context.Context, cmd contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	if _, err := s.Store.CreateExact(ctx, cmd, identity); err != nil {
		return contract.CommandStatus{}, err
	}
	return contract.CommandStatus{}, context.DeadlineExceeded
}

func TestLostCommitIsLoggedWhenStatusIsObserved(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	runID := uuid.New()
	engine, err := demo.NewEngine(context.Background(), &lostCreateCommit{Store: store}, demo.NewSimulator(), runID)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	logDir := t.TempDir()
	logger, err := diagnostics.Open(logDir, diagnostics.RoleCore, runID, io.Discard, diagnostics.Config{AppVersion: "test", ProfileID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	lease, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, feed: newEventFeed(engine), engine: engine, store: store, logger: logger.Application(diagnostics.ComponentCore)}
	id := uuid.New()
	body := `{"commandId":"` + id.String() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	request := func(method, path, body string) *http.Request {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set(leaseHeader, lease.id.String())
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		return req
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request(http.MethodPost, "/v1/tests", body))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("lost response: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request(http.MethodGet, "/v1/commands/"+id.String(), ""))
	if response.Code != http.StatusOK {
		t.Fatalf("status: %d %s", response.Code, response.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(ctx); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(logDir)
	if err != nil || len(files) != 1 {
		t.Fatalf("logs: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(logDir, files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event["command_id"] == id.String() && event["event"] == "command.observed" && event["command_status"] == "completed" {
			found = true
		}
	}
	if !found {
		t.Fatal("observed committed command absent from diagnostics")
	}
}

type heldCreateLookup struct {
	*storage.Store
	block   atomic.Bool
	calls   atomic.Int32
	blockAt int32
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *heldCreateLookup) LookupCreateExact(ctx context.Context, cmd contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	call := s.calls.Add(1)
	if s.block.Load() && strings.Contains(string(identity), "1.0000000000000001") && (s.blockAt == 0 || call == s.blockAt) {
		s.once.Do(func() { close(s.entered) })
		<-s.release
	}
	return s.Store.LookupCreateExact(ctx, cmd, identity)
}

func TestStoredReplayWinsOverConcurrentConflictingLookup(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	body := `{"commandId":"` + id.String() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	var command contract.CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", []byte(body), &command)
	if err != nil || !input.Valid {
		t.Fatalf("input: %v", err)
	}
	created, err := store.CreateExact(context.Background(), command, input.Identity)
	if err != nil {
		t.Fatal(err)
	}
	held := &heldCreateLookup{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	held.block.Store(true)
	engine, err := demo.NewEngine(context.Background(), held, demo.NewSimulator(), uuid.New())
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
	request := func(value string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/tests", strings.NewReader(value))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set(leaseHeader, lease.id.String())
		req.Header.Set("Content-Type", "application/json")
		return req
	}
	conflictDone := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request(strings.Replace(body, `"areaM2":1,`, `"areaM2":1.0000000000000001,`, 1)))
		conflictDone <- response.Code
	}()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("conflicting lookup did not start")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request(body))
	var repeated contract.CommandStatus
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &repeated) != nil || repeated.TestId == nil || created.TestId == nil || *repeated.TestId != *created.TestId {
		t.Fatalf("stored replay: %d %s", response.Code, response.Body.String())
	}
	close(held.release)
	if code := <-conflictDone; code != http.StatusConflict {
		t.Fatalf("conflict: %d", code)
	}
}

type heldCommandRead struct {
	*storage.Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type pairedCreateLookups struct {
	*storage.Store
	calls   atomic.Int32
	entered [2]chan struct{}
	release [2]chan struct{}
}

func (s *pairedCreateLookups) LookupCreateExact(ctx context.Context, cmd contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	result, err := s.Store.LookupCreateExact(ctx, cmd, identity)
	index := s.calls.Add(1)
	if index <= 2 {
		close(s.entered[index-1])
		<-s.release[index-1]
	}
	return result, err
}

func TestConcurrentReplayLogsOneExecution(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	held := &pairedCreateLookups{Store: store, entered: [2]chan struct{}{make(chan struct{}), make(chan struct{})}, release: [2]chan struct{}{make(chan struct{}), make(chan struct{})}}
	runID := uuid.New()
	engine, err := demo.NewEngine(context.Background(), held, demo.NewSimulator(), runID)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	logDir := t.TempDir()
	logger, err := diagnostics.Open(logDir, diagnostics.RoleCore, runID, io.Discard, diagnostics.Config{AppVersion: "test", ProfileID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	lease, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, feed: newEventFeed(engine), engine: engine, store: store, logger: logger.Application(diagnostics.ComponentCore)}
	id := uuid.New()
	body := `{"commandId":"` + id.String() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	send := func(done chan<- int) {
		req := httptest.NewRequest(http.MethodPost, "/v1/tests", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set(leaseHeader, lease.id.String())
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		done <- response.Code
	}
	first, second := make(chan int, 1), make(chan int, 1)
	go send(first)
	select {
	case <-held.entered[0]:
	case <-time.After(5 * time.Second):
		t.Fatal("first lookup did not start")
	}
	go send(second)
	select {
	case <-held.entered[1]:
	case <-time.After(5 * time.Second):
		t.Fatal("second lookup did not start")
	}
	close(held.release[0])
	if code := <-first; code != http.StatusOK {
		t.Fatalf("first create: %d", code)
	}
	close(held.release[1])
	if code := <-second; code != http.StatusOK {
		t.Fatalf("replay: %d", code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(ctx); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(logDir)
	if err != nil || len(files) != 1 {
		t.Fatalf("logs: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(logDir, files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	completed, observed := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event["command_id"] == id.String() && event["event"] == "command.completed" {
			completed++
		}
		if event["command_id"] == id.String() && event["event"] == "command.observed" && event["command_status"] == "completed" {
			observed++
		}
	}
	if completed != 1 || observed != 1 {
		t.Fatalf("diagnostics: completed=%d observed=%d", completed, observed)
	}
}

func (s *heldCommandRead) Command(ctx context.Context, id uuid.UUID) (contract.CommandStatus, error) {
	result, err := s.Store.Command(ctx, id)
	s.once.Do(func() { close(s.entered); <-s.release })
	return result, err
}

func TestCommandStatusRereadsWhenCreateCrossesRead(t *testing.T) {
	testCommandStatusAcrossCreate(t, true)
}

func TestUnrelatedCreateDoesNotInventUnknownCommand(t *testing.T) {
	testCommandStatusAcrossCreate(t, false)
}

func testCommandStatusAcrossCreate(t *testing.T, sameID bool) {
	t.Helper()
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
	held := &heldCommandRead{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, feed: newEventFeed(engine), engine: engine, store: held}
	id := uuid.New()
	createID := id
	if !sameID {
		createID = uuid.New()
	}
	request := func(method, path, body string) *http.Request {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set(leaseHeader, lease.id.String())
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		return req
	}
	statusDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request(http.MethodGet, "/v1/commands/"+id.String(), ""))
		statusDone <- response
	}()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("status read did not start")
	}
	body := `{"commandId":"` + createID.String() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	create := httptest.NewRecorder()
	handler.ServeHTTP(create, request(http.MethodPost, "/v1/tests", body))
	if create.Code != http.StatusOK {
		t.Fatalf("create: %d %s", create.Code, create.Body.String())
	}
	close(held.release)
	response := <-statusDone
	var status contract.CommandStatus
	if sameID && (response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &status) != nil || status.Status != "completed") {
		t.Fatalf("status crossing create: %d %s", response.Code, response.Body.String())
	}
	if !sameID && response.Code != http.StatusNotFound {
		t.Fatalf("unrelated command: %d %s", response.Code, response.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledPrelookupDoesNotHideKnownSnapshot(t *testing.T) {
	testCanceledCreateLookup(t, 0)
}

func TestCanceledEngineLookupDoesNotHideKnownSnapshot(t *testing.T) {
	testCanceledCreateLookup(t, 2)
}

func testCanceledCreateLookup(t *testing.T, blockAt int32) {
	t.Helper()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	held := &heldCreateLookup{Store: store, entered: make(chan struct{}), release: make(chan struct{}), blockAt: blockAt}
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
	created, err := lease.controller.Create(context.Background(), contract.CreateTestCommand{CommandId: uuid.New(), Parameters: contract.TestParameters{Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 1, Direction: "pressure"}})
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, feed: newEventFeed(engine), engine: engine, store: store}
	id := uuid.New()
	body := `{"commandId":"` + id.String() + `","expectedRevision":1,"parameters":{"valve":"V","areaM2":1.0000000000000001,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	request := func(method, path, body string) *http.Request {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set(leaseHeader, lease.id.String())
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		return req
	}
	held.block.Store(true)
	done := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request(http.MethodPost, "/v1/tests", body))
		done <- response.Code
	}()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("lookup did not start")
	}
	select {
	case code := <-done:
		if code != http.StatusServiceUnavailable {
			t.Fatalf("timeout: %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP did not time out")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request(http.MethodGet, "/v1/snapshot", ""))
	var snapshot contract.Snapshot
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &snapshot) != nil || snapshot.ActiveTest == nil || snapshot.ActiveTest.TestId != *created.TestId {
		t.Fatalf("snapshot while lookup is stuck: %d %s", response.Code, response.Body.String())
	}
	workersDone := handler.stopWorkers()
	select {
	case <-workersDone:
		t.Fatal("stuck lookup was reported drained")
	default:
	}
	close(held.release)
	select {
	case <-workersDone:
	case <-time.After(5 * time.Second):
		t.Fatal("lookup worker did not drain")
	}
	if _, err := store.Command(context.Background(), id); !errors.Is(err, storage.ErrCommandNotFound) {
		t.Fatalf("canceled create wrote command: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatal(err)
	}
}

type failedCreateLookup struct {
	*storage.Store
	fail atomic.Bool
}

func (s *failedCreateLookup) LookupCreateExact(ctx context.Context, cmd contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	if s.fail.Load() {
		return contract.CommandStatus{}, storage.ErrStorageUnavailable
	}
	return s.Store.LookupCreateExact(ctx, cmd, identity)
}

func TestCreateLookupStorageFailureStopsActiveRun(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	wrapped := &failedCreateLookup{Store: store}
	engine, err := demo.NewEngine(context.Background(), wrapped, demo.NewSimulator(), uuid.New())
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
	_, err = lease.controller.Start(context.Background(), id, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	action := engine.State().PendingAction
	if action == nil {
		t.Fatal("missing action")
	}
	if _, err := lease.controller.Confirm(context.Background(), id, action.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	wrapped.fail.Store(true)
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, feed: newEventFeed(engine), engine: engine, store: store}
	body := `{"commandId":"` + uuid.NewString() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tests", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set(leaseHeader, lease.id.String())
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("failure: %d %s", response.Code, response.Body.String())
	}
	timer := time.After(5 * time.Second)
	for {
		state := engine.State()
		if state.Stop != nil && state.Stop.Status == "completed" && state.Record.SaveStatus == "saved" {
			break
		}
		select {
		case <-engine.Changes():
		case <-timer:
			t.Fatal("storage failure did not stop run")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatal(err)
	}
}

type heldCreateWrite struct {
	*storage.Store
	entered chan struct{}
	release chan struct{}
}

func (s *heldCreateWrite) CreateExact(ctx context.Context, command contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	close(s.entered)
	<-s.release
	return s.Store.CreateExact(ctx, command, identity)
}

func TestUnknownCreateThenConfirmedSnapshot(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	held := &heldCreateWrite{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(held.release)
		}
	}()
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
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, feed: newEventFeed(engine), engine: engine, store: store}
	commandID := uuid.New()
	body := `{"commandId":"` + commandID.String() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	request := func(method, path, body string) *http.Request {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set(leaseHeader, lease.id.String())
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		return req
	}
	done := make(chan struct{})
	go func() { handler.ServeHTTP(httptest.NewRecorder(), request("POST", "/v1/tests", body)); close(done) }()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("create did not enter")
	}
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, request("GET", "/v1/commands/"+commandID.String(), ""))
	var status contract.CommandStatus
	if statusResponse.Code != http.StatusOK || json.Unmarshal(statusResponse.Body.Bytes(), &status) != nil || status.Status != "unknown" {
		t.Fatalf("unconfirmed status: %d %s", statusResponse.Code, statusResponse.Body.String())
	}
	snapshotResponse := httptest.NewRecorder()
	handler.ServeHTTP(snapshotResponse, request("GET", "/v1/snapshot", ""))
	if snapshotResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("snapshot while unresolved: %d", snapshotResponse.Code)
	}
	close(held.release)
	released = true
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("create did not finish")
	}
	snapshotResponse = httptest.NewRecorder()
	handler.ServeHTTP(snapshotResponse, request("GET", "/v1/snapshot", ""))
	var snapshot contract.Snapshot
	if snapshotResponse.Code != http.StatusOK || json.Unmarshal(snapshotResponse.Body.Bytes(), &snapshot) != nil || snapshot.ActiveTest == nil {
		t.Fatalf("confirmed snapshot: %d %s", snapshotResponse.Code, snapshotResponse.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatal(err)
	}
}

type failedStopWrite struct{ *storage.Store }

func (s *failedStopWrite) FinishStop(context.Context, uuid.UUID, uuid.UUID, storage.StopDetails) (contract.CommandStatus, error) {
	return contract.CommandStatus{}, context.DeadlineExceeded
}

func (s *failedStopWrite) LookupStop(context.Context, uuid.UUID, uuid.UUID) (contract.CommandStatus, error) {
	return contract.CommandStatus{}, storage.ErrStorageUnavailable
}

func TestSnapshotPreservesKnownStopWhenRecoveryFails(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine, err := demo.NewEngine(context.Background(), &failedStopWrite{Store: store}, demo.NewSimulator(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	lease, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := lease.controller.Create(context.Background(), contract.CreateTestCommand{CommandId: uuid.New(), Parameters: contract.TestParameters{Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 1, Direction: "pressure"}})
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %v", err)
	}
	stopID := engine.State().Record.StopCommandId
	if _, err := lease.controller.Stop(context.Background(), *created.TestId, stopID); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		state := engine.State()
		if state.Record != nil && state.Record.SaveStatus == "unknown" && state.Stop != nil && state.Stop.Status == "completed" {
			break
		}
		select {
		case <-engine.Changes():
		case <-deadline:
			t.Fatal("unknown stop state did not arrive")
		}
	}
	for {
		err := engine.Refresh(context.Background())
		if errors.Is(err, storage.ErrStorageUnavailable) {
			break
		}
		if err != nil && !errors.Is(err, demo.ErrBusy) {
			t.Fatalf("refresh: %v", err)
		}
		select {
		case <-time.After(5 * time.Millisecond):
		case <-deadline:
			t.Fatal("stop recovery did not finish")
		}
	}
	handler := &coreHTTP{secret: "secret", leases: leases, feed: newEventFeed(engine), engine: engine, store: store}
	req := httptest.NewRequest(http.MethodGet, "/v1/snapshot", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set(leaseHeader, lease.id.String())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	var snapshot contract.Snapshot
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &snapshot) != nil || snapshot.ActiveTest == nil || snapshot.ActiveTest.Outcome != "interrupted" || snapshot.ActiveTest.SaveStatus != "unknown" {
		t.Fatalf("snapshot: %d %s", response.Code, response.Body.String())
	}
}

type failedCreatedRead struct {
	*storage.Store
	fail bool
}

func (s *failedCreatedRead) Test(ctx context.Context, id uuid.UUID) (contract.TestRecord, error) {
	if s.fail {
		s.fail = false
		return contract.TestRecord{}, context.DeadlineExceeded
	}
	return s.Store.Test(ctx, id)
}

func TestReadinessRecoversConfirmedCreation(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine, err := demo.NewEngine(context.Background(), &failedCreatedRead{Store: store, fail: true}, demo.NewSimulator(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	lease, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := lease.controller.Create(context.Background(), contract.CreateTestCommand{CommandId: uuid.New(), Parameters: contract.TestParameters{Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 1, Direction: "pressure"}})
	if !errors.Is(err, context.DeadlineExceeded) || created.TestId == nil {
		t.Fatalf("create: %+v %v", created, err)
	}
	handler := &coreHTTP{secret: "secret", leases: leases, feed: newEventFeed(engine), engine: engine, store: store}
	req := httptest.NewRequest(http.MethodGet, "/v1/tests/"+created.TestId.String()+"/readiness", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set(leaseHeader, lease.id.String())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	var readiness contract.Readiness
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &readiness) != nil || !readiness.Ready {
		t.Fatalf("readiness: %d %s", response.Code, response.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatal(err)
	}
}

func TestWorkersAreBoundedAndDrainedBeforeShutdown(t *testing.T) {
	handler := &coreHTTP{}
	for range 32 {
		if !handler.beginWorker() {
			t.Fatal("worker was not admitted")
		}
	}
	if handler.beginWorker() {
		t.Fatal("unbounded worker admitted")
	}
	done := handler.stopWorkers()
	select {
	case <-done:
		t.Fatal("workers reported drained before completion")
	default:
	}
	if handler.beginWorker() {
		t.Fatal("worker admitted during shutdown")
	}
	for range 32 {
		handler.endWorker()
	}
	select {
	case <-done:
	default:
		t.Fatal("completed workers were not drained")
	}
}
