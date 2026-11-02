package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

type delayedCloseListener struct {
	net.Listener
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (l *delayedCloseListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &delayedCloseConn{Conn: conn, owner: l}, nil
}

type delayedCloseConn struct {
	net.Conn
	owner *delayedCloseListener
}

func (c *delayedCloseConn) Close() error {
	if c.owner.armed.CompareAndSwap(true, false) {
		close(c.owner.entered)
		<-c.owner.release
	}
	return c.Conn.Close()
}

func TestConcurrentRevocationWaitsForChannelClosure(t *testing.T) {
	var handler http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	listener := &delayedCloseListener{Listener: server.Listener, entered: make(chan struct{}), release: make(chan struct{})}
	server.Listener = listener
	defer server.Close()
	s, err := newSession(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	fake := &routeService{}
	handler = newRoutes(s, fake, fake)
	server.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	headers := http.Header{"Origin": []string{server.URL}}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/api/session/stream", &websocket.DialOptions{HTTPHeader: headers, Subprotocols: []string{"gk80-v1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	listener.armed.Store(true)
	first, second := make(chan struct{}), make(chan struct{})
	go func() { s.revoke(); close(first) }()
	select {
	case <-listener.entered:
	case <-ctx.Done():
		t.Fatal("close did not start")
	}
	go func() { s.revoke(); close(second) }()
	select {
	case <-second:
		close(listener.release)
		t.Fatal("second revoke returned before socket closed")
	case <-time.After(100 * time.Millisecond):
	}
	close(listener.release)
	select {
	case <-first:
	case <-ctx.Done():
		t.Fatal("first revoke blocked")
	}
	select {
	case <-second:
	case <-ctx.Done():
		t.Fatal("second revoke blocked")
	}
}

func TestRevocationWaitsForAlreadyClosingChannel(t *testing.T) {
	var handler http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	listener := &delayedCloseListener{Listener: server.Listener, entered: make(chan struct{}), release: make(chan struct{})}
	server.Listener = listener
	defer server.Close()
	s, err := newSession(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	fake := &routeService{}
	handler = newRoutes(s, fake, fake)
	server.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	headers := http.Header{"Origin": []string{server.URL}}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/api/session/stream", &websocket.DialOptions{HTTPHeader: headers, Subprotocols: []string{"gk80-v1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	listener.armed.Store(true)
	if err := conn.Write(ctx, websocket.MessageText, []byte("unexpected")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-listener.entered:
	case <-ctx.Done():
		t.Fatal("close did not start")
	}
	done := make(chan struct{})
	go func() { s.revoke(); close(done) }()
	select {
	case <-done:
		close(listener.release)
		t.Fatal("revoke returned before socket closed")
	case <-time.After(100 * time.Millisecond):
	}
	close(listener.release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("revoke blocked")
	}
}

func TestBrowserChannelBindsHTTPAndRevokesOnDisconnect(t *testing.T) {
	var handler http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	defer server.Close()
	s, err := newSession(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer s.revoke()
	entered, cancelled := make(chan struct{}), make(chan struct{})
	fake := &routeService{waitFunc: func(ctx context.Context) (contract.ConnectionNotice, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		return contract.ConnectionNotice{}, ctx.Err()
	}}
	handler = newRoutes(s, fake, fake)
	server.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := func(channel string) int {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/status", nil)
		if err != nil {
			t.Fatal(err)
		}

		if channel != "" {
			r.Header.Set("X-GK80-Channel", channel)
		}
		resp, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}
	if got := request(""); got != http.StatusConflict {
		t.Fatalf("missing channel: %d", got)
	}
	dial := func(origin string) (*websocket.Conn, *http.Response, error) {
		h := http.Header{"Origin": []string{origin}}
		return websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/api/session/stream", &websocket.DialOptions{HTTPHeader: h, Subprotocols: []string{"gk80-v1"}})
	}
	if conn, resp, err := dial("http://evil.invalid"); err == nil {
		_ = conn.CloseNow()
		t.Fatal("foreign origin accepted")
	} else if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %v %+v", err, resp)
	}
	conn, _, err := dial(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var welcome struct {
		Channel string `json:"channel"`
	}
	if err := json.Unmarshal(raw, &welcome); err != nil || welcome.Channel == "" {
		t.Fatalf("welcome: %v", err)
	}
	if conn.Subprotocol() != "gk80-v1" {
		t.Fatal("unexpected subprotocol")
	}
	if got := request(welcome.Channel); got != http.StatusOK {
		t.Fatalf("bound request: %d", got)
	}
	readerDone := make(chan error, 1)
	go func() { _, _, err := conn.Read(ctx); readerDone <- err }()
	time.Sleep(2200 * time.Millisecond)
	if err := conn.Ping(ctx); err != nil {
		t.Fatalf("channel stopped responding after handshake deadline: %v", err)
	}
	if got := request("wrong"); got != http.StatusConflict {
		t.Fatalf("wrong channel: %d", got)
	}
	second, _, err := dial(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, busy, err := second.Read(ctx)
	var occupied struct {
		Busy    bool   `json:"busy"`
		Channel string `json:"channel"`
	}
	if err != nil || json.Unmarshal(busy, &occupied) != nil || !occupied.Busy || occupied.Channel != welcome.Channel {
		t.Fatalf("second connection: %s %v", busy, err)
	}
	_ = second.CloseNow()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		r, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/session/wait?after=0&afterSequence=0", nil)

		r.Header.Set("X-GK80-Channel", welcome.Channel)
		resp, err := server.Client().Do(r)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("wait not admitted")
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte("unexpected")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("disconnected channel did not cancel wait")
	}
	select {
	case err := <-readerDone:
		if err == nil {
			t.Fatal("unexpected frame did not close channel")
		}
	case <-ctx.Done():
		t.Fatal("channel did not close")
	}
	select {
	case <-requestDone:
	case <-ctx.Done():
		t.Fatal("cancelled wait did not return")
	}
	if got := request(welcome.Channel); got != http.StatusConflict {
		t.Fatalf("unexpected frame left HTTP active: %d", got)
	}
	for {
		s.mu.Lock()
		closed := s.channel == nil
		s.mu.Unlock()
		if closed {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("channel did not close")
		}
		time.Sleep(10 * time.Millisecond)
	}
	replacement, _, err := dial(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacement.CloseNow() }()
	_, raw, err = replacement.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var renewed struct {
		Channel string `json:"channel"`
	}
	if err := json.Unmarshal(raw, &renewed); err != nil || renewed.Channel == "" || renewed.Channel == welcome.Channel {
		t.Fatalf("renewed channel: %v", err)
	}
	if got := request(welcome.Channel); got != http.StatusConflict {
		t.Fatalf("old channel accepted: %d", got)
	}
	if got := request(renewed.Channel); got != http.StatusOK {
		t.Fatalf("renewed channel: %d", got)
	}
	s.revoke()
	if _, _, err := replacement.Read(ctx); err == nil {
		t.Fatal("revoked channel remained open")
	}
	if got := request(renewed.Channel); got != http.StatusConflict {
		t.Fatalf("request after revoke: %d", got)
	}
}

func TestBrowserChannelRejectsIncompleteUpgradeBodyWithoutHoldingSession(t *testing.T) {
	var handler http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	defer server.Close()
	s, err := newSession(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	fake := &routeService{}
	handler = newRoutes(s, fake, fake)
	server.Start()
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_, err = fmt.Fprintf(conn, "%s /api/session/stream HTTP/1.1\r\nHost: %s\r\nOrigin: http://%s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Protocol: gk80-v1\r\nContent-Length: 100\r\n\r\nx", method, s.host, s.host)
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(conn)
		_ = conn.Close()
		if e, ok := err.(net.Error); ok && e.Timeout() {
			t.Fatal("incomplete upgrade held connection")
		}
		if strings.Contains(string(body), "101 Switching Protocols") {
			t.Fatal("incomplete upgrade accepted")
		}
	}
	done := make(chan struct{})
	go func() { s.revoke(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("incomplete upgrade blocked revocation")
	}
}
