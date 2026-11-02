package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/desktop"
)

type routeService struct {
	mu                           sync.Mutex
	checks, beats, stops         int
	ticket                       string
	testID                       string
	commandID                    uuid.UUID
	createInput                  desktop.CreateCommandInput
	creates                      int
	dispatch                     contract.CommandDispatch
	dispatchErr                  error
	starts, confirms, waits      int
	startInput                   desktop.StartCommandInput
	confirmInput                 desktop.ConfirmCommandInput
	startID, confirmID, actionID string
	after, sequence              int64
	waitFunc                     func(context.Context) (contract.ConnectionNotice, error)
	createFunc                   func(context.Context) (contract.CommandDispatch, error)
}

func (f *routeService) Status() contract.DesktopStatus { return contract.DesktopStatus{Revision: 101} }
func (f *routeService) Verify(context.Context) contract.DesktopStatus {
	return contract.DesktopStatus{Revision: 102}
}
func (f *routeService) SessionView(context.Context) (contract.SessionView, error) {
	return contract.SessionView{Connection: contract.ConnectionState{Revision: 201}}, nil
}
func (f *routeService) ObserveSession(context.Context) (contract.SessionView, error) {
	return contract.SessionView{Connection: contract.ConnectionState{Revision: 202}}, nil
}
func (f *routeService) WaitConnection(ctx context.Context, after, sequence int64) (contract.ConnectionNotice, error) {
	f.mu.Lock()
	f.waits++
	f.after, f.sequence = after, sequence
	fn := f.waitFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return contract.ConnectionNotice{Connection: contract.ConnectionState{Revision: 303}}, nil
}
func (f *routeService) Command(_ context.Context, id string) (contract.CommandStatus, error) {
	parsed, _ := uuid.Parse(id)
	return contract.CommandStatus{CommandId: parsed, Status: contract.CommandStatusStatusUnknown}, nil
}
func (f *routeService) CreateTest(ctx context.Context, input desktop.CreateCommandInput) (contract.CommandDispatch, error) {
	f.mu.Lock()
	f.creates++
	f.createInput = input
	fn, result, err := f.createFunc, f.dispatch, f.dispatchErr
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return result, err
}
func (f *routeService) StartTest(_ context.Context, id string, input desktop.StartCommandInput) (contract.CommandDispatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.startID, f.startInput = id, input
	return f.dispatch, f.dispatchErr
}
func (f *routeService) ConfirmAction(_ context.Context, id, action string, input desktop.ConfirmCommandInput) (contract.CommandDispatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirms++
	f.confirmID, f.actionID, f.confirmInput = id, action, input
	return f.dispatch, f.dispatchErr
}

func (f *routeService) BeginSessionCheck() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return f.ticket, nil
}
func (f *routeService) KeepAlive(_ context.Context, ticket string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ticket != f.ticket {
		return io.ErrUnexpectedEOF
	}
	f.beats++
	return nil
}
func (f *routeService) StopTest(_ context.Context, id string, input desktop.StopCommandInput) (contract.CommandStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	f.testID = id
	f.commandID = input.CommandId
	return contract.CommandStatus{CommandId: input.CommandId, Status: contract.CommandStatusStatusAccepted}, nil
}

func TestHeartbeatAndStopUseProtectedFixedRoutes(t *testing.T) {
	fake := &routeService{ticket: uuid.NewString()}
	var router http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r) }))
	defer server.Close()
	s, code, err := newSession(server.Listener.Addr().String(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cookie, proof, err := s.pair(code)
	if err != nil {
		t.Fatal(err)
	}
	router = newRoutes(s, fake, fake)
	server.Start()
	occupied := httptest.NewRequest(http.MethodGet, server.URL+"/state", nil)
	occupied.AddCookie(cookie)
	occupied.Header.Set("X-GK80-Session", proof)
	var ordinaryReleases []func()
	for i := 0; i < 60; i++ {
		_, release, status := s.admitClass(occupied, ordinaryRequest)
		if status != 0 {
			t.Fatalf("ordinary admission %d: %d", i, status)
		}
		ordinaryReleases = append(ordinaryReleases, release)
	}
	defer func() {
		for _, release := range ordinaryReleases {
			release()
		}
	}()
	request := func(path, body string, authenticated bool) (int, string) {
		r, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Origin", server.URL)
		r.Header.Set("Content-Type", "application/json")
		if authenticated {
			r.AddCookie(cookie)
			r.Header.Set("X-GK80-Session", proof)
		}
		resp, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(data)
	}
	if status, _ := request("/api/session/check", "", false); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status: %d", status)
	}
	if status, _ := request("/api/session/heartbeat", `{"ticket":"`+fake.ticket+`"}`, false); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated heartbeat: %d", status)
	}
	if status, _ := request("/api/tests/"+uuid.NewString()+"/stop", `{"commandId":"`+uuid.NewString()+`"}`, false); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated stop: %d", status)
	}
	fake.mu.Lock()
	unauthenticatedCalls := fake.checks + fake.beats + fake.stops
	fake.mu.Unlock()
	if unauthenticatedCalls != 0 {
		t.Fatal("unauthenticated request reached service")
	}
	var heartbeatReleases []func()
	for i := 0; i < 2; i++ {
		_, release, status := s.admitClass(occupied, heartbeatRequest)
		if status != 0 {
			t.Fatalf("heartbeat reserve: %d", status)
		}
		heartbeatReleases = append(heartbeatReleases, release)
	}
	if status, _ := request("/api/session/check", "", true); status != http.StatusTooManyRequests {
		t.Fatalf("heartbeat limit: %d", status)
	}
	if status, _ := request("/api/session/heartbeat", `{"ticket":"`+fake.ticket+`"}`, true); status != http.StatusTooManyRequests {
		t.Fatalf("heartbeat keepalive limit: %d", status)
	}
	for _, release := range heartbeatReleases {
		release()
	}
	if status, body := request("/api/session/check", "", true); status != http.StatusOK || !strings.Contains(body, fake.ticket) {
		t.Fatalf("check: %d %s", status, body)
	}
	if status, _ := request("/api/session/heartbeat", `{"ticket":"`+fake.ticket+`"}`, true); status != http.StatusNoContent {
		t.Fatalf("heartbeat: %d", status)
	}
	for _, body := range []string{
		`{"Ticket":"` + fake.ticket + `"}`,
		`{"ticket":"` + fake.ticket + `","Ticket":"` + fake.ticket + `"}`,
		`{"ticket":null}`,
	} {
		if status, _ := request("/api/session/heartbeat", body, true); status != http.StatusBadRequest {
			t.Fatalf("invalid heartbeat accepted: %d", status)
		}
	}
	testID, commandID := uuid.NewString(), uuid.New()
	var stopReleases []func()
	for i := 0; i < 2; i++ {
		_, release, status := s.admitClass(occupied, stopRequest)
		if status != 0 {
			t.Fatalf("stop reserve: %d", status)
		}
		stopReleases = append(stopReleases, release)
	}
	if status, _ := request("/api/tests/"+testID+"/stop", `{"commandId":"`+commandID.String()+`"}`, true); status != http.StatusTooManyRequests {
		t.Fatalf("stop limit: %d", status)
	}
	for _, release := range stopReleases {
		release()
	}
	if status, body := request("/api/tests/"+testID+"/stop", `{"commandId":"`+commandID.String()+`"}`, true); status != http.StatusOK || !bytes.Contains([]byte(body), []byte(`"accepted"`)) {
		t.Fatalf("stop: %d %s", status, body)
	}
	fake.mu.Lock()
	correct := fake.checks == 1 && fake.beats == 1 && fake.stops == 1 && fake.testID == testID && fake.commandID == commandID
	fake.mu.Unlock()
	if !correct {
		t.Fatal("unexpected service calls")
	}
	if status, _ := request("/api/tests/"+testID+"/stop", `{"commandId":"`+commandID.String()+`","commandId":"`+commandID.String()+`"}`, true); status != http.StatusBadRequest {
		t.Fatalf("duplicate commandId: %d", status)
	}
	if status, _ := request("/api/tests/"+testID+"/stop", `{"commandId":"00000000-0000-0000-0000-000000000000"}`, true); status != http.StatusBadRequest {
		t.Fatalf("nil command accepted: %d", status)
	}
	fake.mu.Lock()
	stops := fake.stops
	fake.mu.Unlock()
	if stops != 1 {
		t.Fatal("invalid command reached service")
	}
}

func TestCreateRoutePreservesInputAndDelivery(t *testing.T) {
	fake := &routeService{ticket: uuid.NewString(), dispatch: contract.CommandDispatch{Delivery: contract.NotSent}}
	var router http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r) }))
	defer server.Close()
	s, code, err := newSession(server.Listener.Addr().String(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cookie, proof, err := s.pair(code)
	if err != nil {
		t.Fatal(err)
	}
	router = newRoutes(s, fake, fake)
	server.Start()
	id := uuid.NewString()
	body := `{"commandId":"` + id + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1.0000000000000001,"pmaxPa":1000,"measurementSeconds":1,"direction":"vacuum","actuator":false}}`
	var expected desktop.CreateCommandInput
	if err := json.Unmarshal([]byte(body), &expected); err != nil {
		t.Fatal(err)
	}
	request := func(body string) (int, string) {
		r, err := http.NewRequest(http.MethodPost, server.URL+"/api/tests", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.AddCookie(cookie)
		r.Header.Set("X-GK80-Session", proof)
		r.Header.Set("Origin", server.URL)
		r.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(data)
	}
	status, response := request(body)
	if status != http.StatusOK || !strings.Contains(response, `"delivery":"notSent"`) {
		t.Fatalf("notSent lost: %d %s", status, response)
	}
	fake.mu.Lock()
	creates, actual := fake.creates, fake.createInput
	fake.mu.Unlock()
	if creates != 1 || !reflect.DeepEqual(actual, expected) {
		t.Fatal("original command input was not preserved")
	}
	if status, _ := request(`{"commandId":"` + id + `"}`); status != http.StatusBadRequest {
		t.Fatalf("missing fields accepted: %d", status)
	}
	fake.mu.Lock()
	fake.dispatchErr = errors.New("uncertain outcome")
	fake.mu.Unlock()
	status, response = request(body)
	if status != http.StatusServiceUnavailable || strings.Contains(response, `"notSent"`) {
		t.Fatalf("error misreported: %d %s", status, response)
	}
}

func TestDomainRoutesForwardCommandsAndValidateWait(t *testing.T) {
	commandID, testID, actionID := uuid.New(), uuid.NewString(), uuid.NewString()
	completed := contract.CommandStatus{CommandId: commandID, Status: contract.CommandStatusStatusCompleted}
	fake := &routeService{ticket: uuid.NewString(), dispatch: contract.CommandDispatch{Delivery: contract.Response, Command: &completed}}
	var router http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r) }))
	defer server.Close()
	s, code, err := newSession(server.Listener.Addr().String(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cookie, proof, err := s.pair(code)
	if err != nil {
		t.Fatal(err)
	}
	router = newRoutes(s, fake, fake)
	server.Start()
	request := func(method, path, body string, authenticated bool) (int, string) {
		r, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Origin", server.URL)
		r.Header.Set("Content-Type", "application/json")
		if authenticated {
			r.AddCookie(cookie)
			r.Header.Set("X-GK80-Session", proof)
		}
		resp, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(data)
	}
	start := `{"commandId":"` + commandID.String() + `","expectedRevision":0,"valveClosed":false,"connectionConfirmed":true}`
	confirm := `{"commandId":"` + commandID.String() + `","expectedRevision":0}`
	var expectedStart desktop.StartCommandInput
	var expectedConfirm desktop.ConfirmCommandInput
	if err := json.Unmarshal([]byte(start), &expectedStart); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(confirm), &expectedConfirm); err != nil {
		t.Fatal(err)
	}
	startPath := "/api/tests/" + testID + "/start"
	confirmPath := "/api/tests/" + testID + "/actions/" + actionID + "/confirm"
	if status, _ := request(http.MethodPost, startPath, start, false); status != http.StatusUnauthorized {
		t.Fatalf("unauthorized start: %d", status)
	}
	if status, body := request(http.MethodPost, startPath, start, true); status != http.StatusOK || !strings.Contains(body, `"delivery":"response"`) {
		t.Fatalf("start: %d %s", status, body)
	}
	if status, _ := request(http.MethodPost, confirmPath, confirm, true); status != http.StatusOK {
		t.Fatalf("confirm: %d", status)
	}
	if status, body := request(http.MethodGet, "/api/session/wait?after=4&afterSequence=9", "", true); status != http.StatusOK || !strings.Contains(body, `"revision":303`) {
		t.Fatalf("wait: %d %s", status, body)
	}
	for _, query := range []string{"?after=-1", "?after=1&after=2", "?afterSequence=", "?other=1", "?after=9223372036854775808"} {
		if status, _ := request(http.MethodGet, "/api/session/wait"+query, "", true); status != http.StatusBadRequest {
			t.Fatalf("invalid query %s: %d", query, status)
		}
	}
	if status, _ := request(http.MethodHead, "/api/status", "", true); status != http.StatusMethodNotAllowed {
		t.Fatalf("HEAD: %d", status)
	}
	for _, item := range []struct{ path, marker string }{
		{"/api/status", `"revision":101`},
		{"/api/session/view", `"revision":201`},
		{"/api/session/observe", `"revision":202`},
		{"/api/commands/" + commandID.String(), commandID.String()},
	} {
		if status, body := request(http.MethodGet, item.path, "", true); status != http.StatusOK || !strings.Contains(body, item.marker) {
			t.Fatalf("GET %s: %d %s", item.path, status, body)
		}
	}
	if status, body := request(http.MethodPost, "/api/verify", "", true); status != http.StatusOK || !strings.Contains(body, `"revision":102`) {
		t.Fatalf("verify: %d %s", status, body)
	}
	fake.mu.Lock()
	correct := fake.starts == 1 && fake.confirms == 1 && fake.waits == 1 && fake.startID == testID && fake.confirmID == testID && fake.actionID == actionID && fake.after == 4 && fake.sequence == 9 && reflect.DeepEqual(fake.startInput, expectedStart) && reflect.DeepEqual(fake.confirmInput, expectedConfirm)
	fake.mu.Unlock()
	if !correct {
		t.Fatal("domain calls or original inputs did not match")
	}
}

func TestDomainRouteRevocationCancelsCallAndSuppressesLateResult(t *testing.T) {
	for _, mode := range []string{"create", "wait"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan context.Context, 1)
			release := make(chan struct{})
			var releaseOnce sync.Once
			fake := &routeService{ticket: uuid.NewString()}
			if mode == "create" {
				fake.createFunc = func(ctx context.Context) (contract.CommandDispatch, error) {
					entered <- ctx
					<-release
					return contract.CommandDispatch{Delivery: contract.NotSent}, nil
				}
			} else {
				fake.waitFunc = func(ctx context.Context) (contract.ConnectionNotice, error) {
					entered <- ctx
					<-release
					return contract.ConnectionNotice{Connection: contract.ConnectionState{Revision: 987654}}, nil
				}
			}
			var router http.Handler
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r) }))
			defer server.Close()
			defer releaseOnce.Do(func() { close(release) })
			s, code, err := newSession(server.Listener.Addr().String(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			cookie, proof, err := s.pair(code)
			if err != nil {
				t.Fatal(err)
			}
			router = newRoutes(s, fake, fake)
			server.Start()
			method, path, body := http.MethodGet, "/api/session/wait?after=1&afterSequence=2", ""
			if mode == "create" {
				method, path = http.MethodPost, "/api/tests"
				body = `{"commandId":"` + uuid.NewString() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
			}
			r, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			r.AddCookie(cookie)
			r.Header.Set("X-GK80-Session", proof)
			r.Header.Set("Origin", server.URL)
			r.Header.Set("Content-Type", "application/json")
			type response struct {
				status int
				body   string
				err    error
			}
			result := make(chan response, 1)
			go func() {
				resp, err := server.Client().Do(r)
				if err != nil {
					result <- response{err: err}
					return
				}
				defer func() { _ = resp.Body.Close() }()
				data, err := io.ReadAll(resp.Body)
				result <- response{status: resp.StatusCode, body: string(data), err: err}
			}()
			var ctx context.Context
			select {
			case ctx = <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("service not reached")
			}
			revoked := make(chan struct{})
			go func() { s.revoke(); close(revoked) }()
			select {
			case <-revoked:
			case <-time.After(3 * time.Second):
				t.Fatal("revocation waited for blocked service")
			}
			if ctx.Err() == nil {
				t.Fatal("service context not cancelled")
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case got := <-result:
				if got.status >= 200 && got.status < 300 {
					t.Fatalf("late result delivered: %+v", got)
				}
				if strings.Contains(got.body, "987654") || strings.Contains(got.body, "notSent") {
					t.Fatalf("late data leaked: %+v", got)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("HTTP request did not finish")
			}
		})
	}
}

type observedRequestBody struct {
	io.ReadCloser
	reads      atomic.Int32
	secondRead chan struct{}
}

func (b *observedRequestBody) Read(p []byte) (int, error) {
	if b.reads.Add(1) == 2 {
		close(b.secondRead)
	}
	return b.ReadCloser.Read(p)
}

func TestRevocationDuringBodyReadDoesNotCallService(t *testing.T) {
	for _, path := range []string{"/api/session/heartbeat", "/api/tests/" + uuid.NewString() + "/stop"} {
		t.Run(path, func(t *testing.T) {
			fake := &routeService{ticket: uuid.NewString()}
			body := `{"ticket":"` + fake.ticket + `"}`
			if path != "/api/session/heartbeat" {
				body = `{"commandId":"` + uuid.NewString() + `"}`
			}
			secondRead, handlerDone := make(chan struct{}), make(chan struct{})
			var router http.Handler
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handlerDone)
				r.Body = &observedRequestBody{ReadCloser: r.Body, secondRead: secondRead}
				router.ServeHTTP(w, r)
			}))
			defer server.Close()
			s, code, err := newSession(server.Listener.Addr().String(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			cookie, proof, err := s.pair(code)
			if err != nil {
				t.Fatal(err)
			}
			router = newRoutes(s, fake, fake)
			server.Start()
			conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			_, err = fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: %s\r\nOrigin: http://%s\r\nCookie: %s=%s\r\nX-GK80-Session: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", path, s.host, s.host, cookie.Name, cookie.Value, proof, len(body), body[:1])
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-secondRead:
			case <-time.After(time.Second):
				t.Fatal("handler did not wait for rest of body")
			}
			s.revoke()
			_, _ = conn.Write([]byte(body[1:]))
			select {
			case <-handlerDone:
			case <-time.After(time.Second):
				t.Fatal("cancelled body read did not finish")
			}
			fake.mu.Lock()
			called := fake.beats != 0 || fake.stops != 0
			fake.mu.Unlock()
			if called {
				t.Fatal("service called after revocation during body read")
			}
		})
	}
}

func TestRejectedIncompletePostDoesNotHoldConnection(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/session/heartbeat"},
		{http.MethodPost, "/api/unknown"},
		{http.MethodPost, "/api//session/heartbeat"},
		{http.MethodGet, "/api/session/heartbeat"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			fake := &routeService{ticket: uuid.NewString()}
			var router http.Handler
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r) }))
			defer server.Close()
			s, _, err := newSession(server.Listener.Addr().String(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			router = newRoutes(s, fake, fake)
			server.Start()
			conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			_, err = fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: %s\r\nOrigin: http://%s\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{", tc.method, tc.path, s.host, s.host)
			if err != nil {
				t.Fatal(err)
			}
			if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			_, err = io.ReadAll(conn)
			if e, ok := err.(net.Error); ok && e.Timeout() {
				t.Fatal("rejected request held connection open")
			}
		})
	}
}

func TestCancelledAdmissionDoesNotHoldIncompletePost(t *testing.T) {
	fake := &routeService{ticket: uuid.NewString()}
	var router http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		cancel()
		router.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer server.Close()
	s, code, err := newSession(server.Listener.Addr().String(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cookie, proof, err := s.pair(code)
	if err != nil {
		t.Fatal(err)
	}
	router = newRoutes(s, fake, fake)
	server.Start()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, err = fmt.Fprintf(conn, "POST /api/session/heartbeat HTTP/1.1\r\nHost: %s\r\nOrigin: http://%s\r\nCookie: %s=%s\r\nX-GK80-Session: %s\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{", s.host, s.host, cookie.Name, cookie.Value, proof)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(conn)
	if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("cancelled request held connection open")
	}
}
