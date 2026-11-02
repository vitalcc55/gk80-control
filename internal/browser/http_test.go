package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type delayedWriter struct {
	header       http.Header
	started      chan struct{}
	unblock      chan struct{}
	deadlineSet  chan struct{}
	deadlineOnce sync.Once
}

func (w *delayedWriter) Header() http.Header { return w.header }
func (w *delayedWriter) WriteHeader(int)     {}
func (w *delayedWriter) Write([]byte) (int, error) {
	close(w.started)
	<-w.unblock
	return 0, errors.New("closed")
}
func (w *delayedWriter) FlushError() error { return nil }
func (w *delayedWriter) SetWriteDeadline(deadline time.Time) error {
	if !deadline.After(time.Now()) {
		w.deadlineOnce.Do(func() { close(w.deadlineSet) })
	}
	return nil
}
func (w *delayedWriter) SetReadDeadline(time.Time) error { return nil }

func TestRevocationWaitsForAlreadyAdmittedResponse(t *testing.T) {
	s, generation := testSession(t, "127.0.0.1:19080")
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19080/state", nil)
	r.Header.Set("X-GK80-Channel", generation)
	w := &delayedWriter{header: make(http.Header), started: make(chan struct{}), unblock: make(chan struct{}), deadlineSet: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = recover() }()
		s.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.writeJSON(w, r, http.StatusOK, []byte(`{"result":"ok"}`))
		})).ServeHTTP(w, r)
	}()
	select {
	case <-w.started:
	case <-time.After(time.Second):
		t.Fatal("response did not start")
	}
	revoked := make(chan struct{})
	go func() { s.revoke(); close(revoked) }()
	select {
	case <-w.deadlineSet:
	case <-time.After(time.Second):
		t.Fatal("revocation did not interrupt response")
	}
	select {
	case <-revoked:
		t.Fatal("revocation returned before response ended")
	default:
	}
	close(w.unblock)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not end")
	}
	select {
	case <-revoked:
	case <-time.After(time.Second):
		t.Fatal("revocation did not finish")
	}
}

func TestRevokedSessionCannotStartLateHTTPResponse(t *testing.T) {
	entered := make(chan struct{})
	continueHandler := make(chan struct{})
	var release sync.Once
	var s *browserSession
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-continueHandler
			s.writeJSON(w, r, http.StatusOK, []byte(`{"private":"result"}`))
		})).ServeHTTP(w, r)
	}))
	defer server.Close()
	defer release.Do(func() { close(continueHandler) })
	initial, generation := testSession(t, server.Listener.Addr().String())
	s = initial
	server.Start()
	r, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/state", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-GK80-Channel", generation)
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
		body, _ := io.ReadAll(resp.Body)
		result <- response{status: resp.StatusCode, body: string(body)}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler not reached")
	}
	s.revoke()
	release.Do(func() { close(continueHandler) })
	select {
	case value := <-result:
		if strings.Contains(value.body, "private") || (value.err == nil && value.status >= 200 && value.status < 300) {
			t.Fatalf("revoked response was successful or leaked: %+v", value)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("response did not terminate")
	}
}

func TestRevocationInterruptsResponseWithIncompleteRequestBody(t *testing.T) {
	var s *browserSession
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.writeJSON(w, r, http.StatusOK, []byte(`{"private":"result"}`))
		})).ServeHTTP(w, r)
	}))
	defer server.Close()
	initial, generation := testSession(t, server.Listener.Addr().String())
	s = initial
	server.Start()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, err = fmt.Fprintf(conn, "POST /state HTTP/1.1\r\nHost: %s\r\nOrigin: http://%s\r\nX-GK80-Channel: %s\r\nContent-Length: 1000\r\n\r\nx", s.host, s.host, generation)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		started := len(s.writes) > 0
		s.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("response did not enter write barrier")
		}
		runtime.Gosched()
	}
	done := make(chan struct{})
	go func() { s.revoke(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("revocation blocked on unread request body")
	}
}

func TestAllowedSessionReceivesBoundedJSONFromHTTPServer(t *testing.T) {
	var s *browserSession
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.writeJSON(w, r, http.StatusOK, []byte(`{"state":"ready"}`))
		})).ServeHTTP(w, r)
	}))
	defer server.Close()
	initial, generation := testSession(t, server.Listener.Addr().String())
	s = initial
	server.Start()
	r, err := http.NewRequest(http.MethodGet, server.URL+"/state", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-GK80-Channel", generation)
	resp, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != `{"state":"ready"}` {
		t.Fatalf("unexpected response: %d %q %v", resp.StatusCode, body, err)
	}
}

func TestOversizedResponseIsNotPartiallyDelivered(t *testing.T) {
	var s *browserSession
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.writeJSON(w, r, http.StatusOK, []byte(strings.Repeat("x", maxResponseBytes+1)))
		})).ServeHTTP(w, r)
	}))
	defer server.Close()
	initial, generation := testSession(t, server.Listener.Addr().String())
	s = initial
	server.Start()
	r, err := http.NewRequest(http.MethodGet, server.URL+"/state", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-GK80-Channel", generation)
	resp, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusInternalServerError || string(body) != `{"error":"response too large"}` {
		t.Fatalf("oversized response was partially delivered: %d %q %v", resp.StatusCode, body, err)
	}
}
