package browser

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
	mu                   sync.Mutex
	checks, beats, stops int
	ticket               string
	testID               string
	commandID            uuid.UUID
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
	router = newRoutes(s, fake)
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
			router = newRoutes(s, fake)
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
			router = newRoutes(s, fake)
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
	router = newRoutes(s, fake)
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
