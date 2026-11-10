package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/desktop"
)

// processBrowser uses the real routes and owner.
type processBrowser struct {
	owner   *desktop.Owner
	server  *httptest.Server
	session *browserSession
	channel string
	conn    *websocket.Conn
	stop    chan struct{}
	done    chan error
	once    sync.Once
	err     error
	creates atomic.Int32
	lost    chan error
}

// lostResponseWriter closes one response after the real service returns and
// before its headers reach the client. It is used only by this process test.
type lostResponseWriter struct {
	http.ResponseWriter
	result chan<- error
}

func (w *lostResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *lostResponseWriter) FlushError() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *lostResponseWriter) WriteHeader(status int) {
	if status != http.StatusOK {
		w.result <- fmt.Errorf("unexpected create HTTP status: %d", status)
		w.ResponseWriter.WriteHeader(status)
		return
	}
	conn, _, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		err = conn.Close()
	}
	w.result <- err
	panic(http.ErrAbortHandler)
}

func (p *processBrowser) request(method, path string, body any) (int, []byte, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
	}
	req, err := http.NewRequest(method, p.server.URL+path, bytes.NewReader(data))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-GK80-Channel", p.channel)
	if method == http.MethodPost {
		req.Header.Set("Origin", p.server.URL)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	response, err := p.server.Client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	data, err = io.ReadAll(response.Body)
	return response.StatusCode, data, err
}

func (p *processBrowser) exact(database, kind, testID, actionID, commandID string, body []byte) (contract.ExactLookupResult, error) {
	var result contract.ExactLookupResult
	req, err := http.NewRequest(http.MethodPost, p.server.URL+"/api/commands/"+commandID+"/lookup-exact", bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	req.Header.Set("X-GK80-Channel", p.channel)
	req.Header.Set("Origin", p.server.URL)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GK80-Database", database)
	req.Header.Set("X-GK80-Kind", kind)
	if testID != "" {
		req.Header.Set("X-GK80-Test", testID)
	}
	if actionID != "" {
		req.Header.Set("X-GK80-Action", actionID)
	}
	response, err := p.server.Client().Do(req)
	if err != nil {
		return result, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return result, fmt.Errorf("exact lookup HTTP %d", response.StatusCode)
	}
	err = json.NewDecoder(response.Body).Decode(&result)
	return result, err
}

func (p *processBrowser) heartbeat() error {
	status, data, err := p.request(http.MethodPost, "/api/session/check", nil)
	if err != nil || status != http.StatusOK {
		return fmt.Errorf("session check: status %d, error %v", status, err)
	}
	var ticket struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(data, &ticket); err != nil || ticket.Ticket == "" {
		return fmt.Errorf("invalid session ticket: %v", err)
	}
	status, _, err = p.request(http.MethodPost, "/api/session/heartbeat", ticket)
	if err != nil || status != http.StatusNoContent {
		return fmt.Errorf("session heartbeat: status %d, error %v", status, err)
	}
	return nil
}

func (p *processBrowser) close() error {
	p.once.Do(func() {
		if p.stop != nil {
			close(p.stop)
			if err := <-p.done; err != nil {
				p.err = err
			}
		}
		if p.session != nil {
			p.session.revoke()
		}
		if p.server != nil {
			p.server.Close()
		}
		p.err = errors.Join(p.err, p.owner.Close())
		if !p.owner.CoreExitConfirmed() {
			p.err = errors.Join(p.err, errors.New("owned core exit not confirmed"))
		}
	})
	return p.err
}

func startProcessBrowser(t *testing.T, executable, profile string, loseCreate bool) *processBrowser {
	t.Helper()
	p := &processBrowser{owner: desktop.NewOwner(uuid.New(), executable, profile, "test", "0.1.0", slog.New(slog.NewTextHandler(io.Discard, nil)), nil, false, func(contract.DesktopStatus) {})}
	t.Cleanup(func() {
		if err := p.close(); err != nil {
			t.Errorf("close process browser: %v", err)
		}
	})
	p.owner.Start()
	deadline := time.Now().Add(15 * time.Second)
	for {
		status := p.owner.Status()
		if status.Status == contract.DesktopStatusStatusReady && status.Handshake != nil {
			break
		}
		if status.Status != contract.DesktopStatusStatusStarting || time.Now().After(deadline) {
			t.Fatalf("core startup: %+v", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var handler http.Handler
	if loseCreate {
		p.lost = make(chan error, 1)
	}
	p.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/tests" {
			if p.creates.Add(1) == 1 && p.lost != nil {
				w = &lostResponseWriter{ResponseWriter: w, result: p.lost}
			}
		}
		handler.ServeHTTP(w, r)
	}))
	var err error
	p.session, err = newSession(p.server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	handler = newRoutes(p.session, desktop.NewService(p.owner), desktop.NewBootstrapService(p.owner))
	p.server.Start()
	p.server.Client().Timeout = 4 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.conn, _, err = websocket.Dial(ctx, "ws"+strings.TrimPrefix(p.server.URL, "http")+"/api/session/stream", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{p.server.URL}}, Subprotocols: []string{"gk80-v1"}})
	if err != nil {
		t.Fatal(err)
	}
	_, welcome, err := p.conn.Read(ctx)
	var opened struct {
		Channel string `json:"channel"`
	}
	if err != nil || json.Unmarshal(welcome, &opened) != nil || opened.Channel == "" {
		t.Fatal("browser connection unavailable")
	}
	p.channel = opened.Channel
	if err := p.heartbeat(); err != nil {
		t.Fatal(err)
	}
	p.stop, p.done = make(chan struct{}), make(chan error, 1)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-p.stop:
				p.done <- nil
				return
			case <-ticker.C:
				if err := p.heartbeat(); err != nil {
					p.done <- err
					return
				}
			}
		}
	}()
	return p
}

func processRequest(t *testing.T, p *processBrowser, method, path string, body, result any, expected int) {
	t.Helper()
	status, data, err := p.request(method, path, body)
	if err != nil || status != expected {
		t.Fatalf("%s %s: status %d, error %v, body %s", method, path, status, err, data)
	}
	if result != nil {
		value := reflect.ValueOf(result)
		if value.Kind() != reflect.Pointer || value.IsNil() {
			t.Fatal("result must be a non-nil pointer")
		}
		value.Elem().SetZero()
		if err := json.Unmarshal(data, result); err != nil {
			t.Fatalf("%s %s: invalid JSON: %v", method, path, err)
		}
	}
}

func waitProcessView(t *testing.T, p *processBrowser, name string, condition func(contract.SessionView) bool) contract.SessionView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var view contract.SessionView
		processRequest(t, p, http.MethodGet, "/api/session/observe", nil, &view, http.StatusOK)
		if condition(view) {
			return view
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: last view %+v", name, view)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func TestBrowserRoutesRunAndReopenCore(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "gk80-core")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", executable, "./cmd/gk80-core")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build core: %v %s", err, output)
	}
	profile := t.TempDir()
	p := startProcessBrowser(t, executable, profile, true)
	var status contract.DesktopStatus
	processRequest(t, p, http.MethodGet, "/api/status", nil, &status, http.StatusOK)
	if status.Handshake == nil {
		t.Fatal("missing handshake")
	}
	handshake := *status.Handshake
	view := waitProcessView(t, p, "initial live", func(v contract.SessionView) bool { return v.Connection.Status == contract.Live && v.Snapshot != nil })
	create := contract.CreateTestCommand{CommandId: uuid.New(), ExpectedRevision: view.Snapshot.Revision, Parameters: contract.TestParameters{PreOpenStabilizationSeconds: 10, PostCloseStabilizationSeconds: 10, Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 60, Direction: "pressure"}}
	createBody, err := json.Marshal(create)
	if err != nil {
		t.Fatal(err)
	}
	statusCode, _, requestErr := p.request(http.MethodPost, "/api/tests", create)
	if requestErr == nil || statusCode != 0 {
		t.Fatalf("create response was not lost: status=%d error=%v", statusCode, requestErr)
	}
	select {
	case err := <-p.lost:
		if err != nil {
			t.Fatalf("drop create response: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("create response was not intercepted")
	}
	lookup, err := p.exact(handshake.DatabaseId.String(), "create", "", "", create.CommandId.String(), createBody)
	if err != nil || lookup.Outcome != contract.ExactLookupResultOutcomeMatch || lookup.Command == nil || lookup.Command.CommandId != create.CommandId || lookup.Command.Status != contract.CommandStatusStatusCompleted || lookup.Command.TestId == nil {
		t.Fatalf("created command after lost response: %+v %v", lookup, err)
	}
	command := *lookup.Command
	id := command.TestId.String()
	changedBody := []byte(strings.Replace(string(createBody), `"areaM2":1`, `"areaM2":1.0000000000000001`, 1))
	if string(changedBody) == string(createBody) {
		t.Fatal("expected area missing")
	}
	lookup, err = p.exact(handshake.DatabaseId.String(), "create", "", "", create.CommandId.String(), changedBody)
	if err != nil || lookup.Outcome != contract.ExactLookupResultOutcomeConflict || lookup.Command != nil {
		t.Fatalf("exact conflict: %+v %v", lookup, err)
	}
	lookup, err = p.exact(uuid.NewString(), "create", "", "", create.CommandId.String(), createBody)
	if err != nil || lookup.Outcome != contract.ExactLookupResultOutcomeDatabaseMismatch || lookup.Command != nil {
		t.Fatalf("other database: %+v %v", lookup, err)
	}
	if count := p.creates.Load(); count != 1 {
		t.Fatalf("create POST count after recovery: %d", count)
	}
	var record contract.TestRecord
	processRequest(t, p, http.MethodGet, "/api/tests/"+id, nil, &record, http.StatusOK)
	processRequest(t, p, http.MethodGet, "/api/commands/"+record.StopCommandId.String(), nil, &command, http.StatusOK)
	if command.Status != contract.CommandStatusStatusUnknown {
		t.Fatalf("reserved stop: %+v", command)
	}
	var ready contract.Readiness
	processRequest(t, p, http.MethodGet, "/api/tests/"+id+"/readiness", nil, &ready, http.StatusOK)
	if !ready.Ready {
		t.Fatalf("not ready: %+v", ready)
	}
	waitProcessView(t, p, "live after create", func(v contract.SessionView) bool {
		return v.Connection.Status == contract.Live && v.Snapshot != nil && v.Snapshot.ActiveTest != nil && v.Snapshot.ActiveTest.TestId.String() == id
	})
	start := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: ready.Revision, ValveClosed: true, ConnectionConfirmed: true}
	var started contract.CommandDispatch
	processRequest(t, p, http.MethodPost, "/api/tests/"+id+"/start", start, &started, http.StatusOK)
	if started.Delivery != contract.CommandDispatchDeliveryResponse || started.Command == nil || started.Command.Status != contract.CommandStatusStatusAccepted {
		t.Fatalf("start: %+v", started)
	}
	startBody, err := json.Marshal(start)
	if err != nil {
		t.Fatal(err)
	}
	lookup, err = p.exact(handshake.DatabaseId.String(), "start", id, "", start.CommandId.String(), startBody)
	if err != nil || lookup.Outcome != contract.ExactLookupResultOutcomeMatch || lookup.Command == nil || lookup.Command.CommandId != start.CommandId || lookup.Command.Status == contract.CommandStatusStatusUnknown {
		t.Fatalf("exact start: %+v %v", lookup, err)
	}
	lookup, err = p.exact(handshake.DatabaseId.String(), "start", uuid.NewString(), "", start.CommandId.String(), startBody)
	if err != nil || lookup.Outcome != contract.ExactLookupResultOutcomeConflict || lookup.Command != nil {
		t.Fatalf("start wrong test: %+v %v", lookup, err)
	}
	view = waitProcessView(t, p, "pending action", func(v contract.SessionView) bool {
		return v.Connection.Status == contract.Live && v.Snapshot != nil && v.Snapshot.PendingAction != nil
	})
	confirm := contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: view.Snapshot.Revision}
	var confirmed contract.CommandDispatch
	processRequest(t, p, http.MethodPost, "/api/tests/"+id+"/actions/"+view.Snapshot.PendingAction.ActionId.String()+"/confirm", confirm, &confirmed, http.StatusOK)
	if confirmed.Delivery != contract.CommandDispatchDeliveryResponse || confirmed.Command == nil || confirmed.Command.Status != contract.CommandStatusStatusCompleted {
		t.Fatalf("confirm: %+v", confirmed)
	}
	confirmBody, err := json.Marshal(confirm)
	if err != nil {
		t.Fatal(err)
	}
	actionID := view.Snapshot.PendingAction.ActionId.String()
	lookup, err = p.exact(handshake.DatabaseId.String(), "confirm", id, actionID, confirm.CommandId.String(), confirmBody)
	if err != nil || lookup.Outcome != contract.ExactLookupResultOutcomeMatch || lookup.Command == nil || lookup.Command.CommandId != confirm.CommandId || lookup.Command.Status != contract.CommandStatusStatusCompleted {
		t.Fatalf("exact confirm: %+v %v", lookup, err)
	}
	lookup, err = p.exact(handshake.DatabaseId.String(), "confirm", id, uuid.NewString(), confirm.CommandId.String(), confirmBody)
	if err != nil || lookup.Outcome != contract.ExactLookupResultOutcomeConflict || lookup.Command != nil {
		t.Fatalf("confirm wrong action: %+v %v", lookup, err)
	}
	view = waitProcessView(t, p, "measurements", func(v contract.SessionView) bool {
		return v.Snapshot != nil && v.Snapshot.ActiveTest != nil && len(v.Snapshot.Measurements) > 0
	})
	if view.Snapshot.Measurements[0].Source != "simulator" {
		t.Fatalf("measurement: %+v", view.Snapshot.Measurements[0])
	}
	stopID := view.Snapshot.ActiveTest.StopCommandId
	stop := contract.StopTestCommand{CommandId: stopID}
	stopJSON, err := json.Marshal(stop)
	if err != nil {
		t.Fatal(err)
	}
	var stopped contract.CommandDispatch
	processRequest(t, p, http.MethodPost, "/api/tests/"+id+"/stop", stop, &stopped, http.StatusOK)
	if stopped.Delivery != contract.CommandDispatchDeliveryResponse || stopped.Command == nil {
		t.Fatalf("stop dispatch: %+v", stopped)
	}
	command = *stopped.Command
	if command.Status != contract.CommandStatusStatusAccepted && command.Status != contract.CommandStatusStatusCompleted {
		t.Fatalf("stop: %+v", command)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		processRequest(t, p, http.MethodGet, "/api/commands/"+stopID.String(), nil, &command, http.StatusOK)
		processRequest(t, p, http.MethodGet, "/api/tests/"+id, nil, &record, http.StatusOK)
		if command.Status == contract.CommandStatusStatusCompleted && record.Stage == "finished" && record.SaveStatus == "saved" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stop not saved: command=%+v record=%+v", command, record)
		}
		time.Sleep(30 * time.Millisecond)
	}
	if record.Outcome != "interrupted" || record.DataQuality != "incomplete" || record.Parameters != create.Parameters || !record.Demo || record.OperatorId != "demo-operator" {
		t.Fatalf("saved record: %+v", record)
	}
	lookup, err = p.exact(handshake.DatabaseId.String(), "stop", id, "", stopID.String(), stopJSON)
	if err != nil || lookup.Outcome != contract.ExactLookupResultOutcomeMatch || lookup.Command == nil || lookup.Command.Status != contract.CommandStatusStatusCompleted {
		t.Fatalf("exact stop: %+v %v", lookup, err)
	}
	lookup, err = p.exact(handshake.DatabaseId.String(), "stop", uuid.NewString(), "", stopID.String(), stopJSON)
	if err != nil || lookup.Outcome != contract.ExactLookupResultOutcomeConflict {
		t.Fatalf("exact stop wrong test: %+v %v", lookup, err)
	}
	if count := p.creates.Load(); count != 1 {
		t.Fatalf("create POST count before shutdown: %d", count)
	}
	if err := p.close(); err != nil {
		t.Fatalf("first core exit: %v", err)
	}
	p = startProcessBrowser(t, executable, profile, false)
	processRequest(t, p, http.MethodGet, "/api/status", nil, &status, http.StatusOK)
	if status.Handshake == nil || status.Handshake.DatabaseId != handshake.DatabaseId || status.Handshake.BackendRunId == handshake.BackendRunId {
		t.Fatalf("reopened handshake: %+v", status.Handshake)
	}
	waitProcessView(t, p, "reopened live", func(v contract.SessionView) bool { return v.Connection.Status == contract.Live && v.Snapshot != nil })
	lookup, err = p.exact(handshake.DatabaseId.String(), "stop", id, "", stopID.String(), stopJSON)
	if err != nil || lookup.Outcome != contract.ExactLookupResultOutcomeMatch || lookup.Command == nil || lookup.Command.Status != contract.CommandStatusStatusCompleted {
		t.Fatalf("exact stop after reopen: %+v %v", lookup, err)
	}
	lookup, err = p.exact(handshake.DatabaseId.String(), "create", "", "", create.CommandId.String(), createBody)
	if err != nil || lookup.Outcome != contract.ExactLookupResultOutcomeMatch || lookup.Command == nil || lookup.Command.TestId == nil || lookup.Command.TestId.String() != id {
		t.Fatalf("exact after reopen: %+v %v", lookup, err)
	}
	lookup, err = p.exact(handshake.DatabaseId.String(), "create", "", "", create.CommandId.String(), changedBody)
	if err != nil || lookup.Outcome != contract.ExactLookupResultOutcomeConflict {
		t.Fatalf("exact conflict after reopen: %+v %v", lookup, err)
	}
	missingID := uuid.NewString()
	missingBody := []byte(strings.Replace(string(createBody), create.CommandId.String(), missingID, 1))
	lookup, err = p.exact(handshake.DatabaseId.String(), "create", "", "", missingID, missingBody)
	if err != nil || lookup.Outcome != contract.ExactLookupResultOutcomeUnknown || lookup.Command != nil {
		t.Fatalf("missing exact: %+v %v", lookup, err)
	}
	changed := create
	changed.Parameters.PmaxPa++
	var conflict contract.CommandDispatch
	processRequest(t, p, http.MethodPost, "/api/tests", changed, &conflict, http.StatusOK)
	if conflict.Delivery != contract.CommandDispatchDeliveryConflict || conflict.Command != nil {
		t.Fatalf("conflict: %+v", conflict)
	}
	var created contract.CommandDispatch
	processRequest(t, p, http.MethodPost, "/api/tests", create, &created, http.StatusOK)
	if created.Delivery != contract.CommandDispatchDeliveryResponse || created.Command == nil || created.Command.CommandId != create.CommandId || created.Command.Status != contract.CommandStatusStatusCompleted || created.Command.TestId == nil || created.Command.TestId.String() != id {
		t.Fatalf("replay: %+v", created)
	}
	var history contract.TestList
	processRequest(t, p, http.MethodGet, "/api/tests", nil, &history, http.StatusOK)
	if len(history.Items) != 1 || history.Items[0].TestId.String() != id {
		t.Fatalf("history: %+v", history)
	}
	processRequest(t, p, http.MethodGet, "/api/tests/"+id, nil, &record, http.StatusOK)
	if record.Outcome != "interrupted" || record.DataQuality != "incomplete" || record.SaveStatus != "saved" || record.Parameters != create.Parameters || record.OperatorId != "demo-operator" {
		t.Fatalf("reopened record: %+v", record)
	}
	var measurements contract.MeasurementPage
	processRequest(t, p, http.MethodGet, "/api/tests/"+id+"/measurements", nil, &measurements, http.StatusOK)
	if len(measurements.Items) == 0 || measurements.Items[0].Source != "simulator" || measurements.Items[0].Quality != "valid" || measurements.Items[0].Unit != "Pa" || measurements.Items[0].MeasuredAt == nil || measurements.Items[0].Value == nil || measurements.Items[0].ObservedAt.IsZero() {
		t.Fatalf("reopened measurements: %+v", measurements)
	}
	var audit contract.AuditPage
	processRequest(t, p, http.MethodGet, "/api/tests/"+id+"/audit", nil, &audit, http.StatusOK)
	counts := map[string]int{}
	for _, event := range audit.Items {
		if event.TestId.String() != id {
			t.Fatalf("audit belongs to another test: %+v", event)
		}
		if event.CommandId != nil {
			counts[event.Action+":"+event.Outcome+":"+event.CommandId.String()]++
		}
	}
	for _, key := range []string{"create:completed:" + create.CommandId.String(), "start:accepted:" + start.CommandId.String(), "confirm:completed:" + confirm.CommandId.String(), "stop:accepted:" + stopID.String(), "stop:completed:" + stopID.String()} {
		if counts[key] != 1 {
			t.Fatalf("audit %s count=%d: %+v", key, counts[key], audit.Items)
		}
	}
	processRequest(t, p, http.MethodGet, "/api/commands/"+stopID.String(), nil, &command, http.StatusOK)
	if command.Status != contract.CommandStatusStatusUnknown {
		t.Fatalf("generic lookup must not identify reopened Stop: %+v", command)
	}
	// An explicit historical replay may remain unconfirmed at the owner boundary.
	// It must never be interpreted as a new, successful stop.
	stopStatus, stopBody, stopErr := p.request(http.MethodPost, "/api/tests/"+id+"/stop", contract.StopTestCommand{CommandId: stopID})
	if stopErr != nil {
		t.Fatalf("historical stop replay: %v", stopErr)
	}
	switch stopStatus {
	case http.StatusServiceUnavailable:
		t.Log("historical Stop replay remains unconfirmed by the owner")
	case http.StatusOK:
		var repeated contract.CommandDispatch
		if err := json.Unmarshal(stopBody, &repeated); err != nil || repeated.Delivery != contract.CommandDispatchDeliveryResponse || repeated.Command == nil || repeated.Command.CommandId != stopID || repeated.Command.TestId == nil || repeated.Command.TestId.String() != id || repeated.Command.Status != contract.CommandStatusStatusCompleted {
			t.Fatalf("historical stop replay: %s, %v", stopBody, err)
		}
	default:
		t.Fatalf("historical stop replay: status %d, body %s", stopStatus, stopBody)
	}
	var afterRecord contract.TestRecord
	var afterAudit contract.AuditPage
	processRequest(t, p, http.MethodGet, "/api/tests/"+id, nil, &afterRecord, http.StatusOK)
	processRequest(t, p, http.MethodGet, "/api/tests/"+id+"/audit", nil, &afterAudit, http.StatusOK)
	if !reflect.DeepEqual(record, afterRecord) || !reflect.DeepEqual(audit, afterAudit) {
		t.Fatal("historical Stop replay changed the saved record or audit")
	}
	if err := p.close(); err != nil {
		t.Fatalf("second core exit: %v", err)
	}
}
