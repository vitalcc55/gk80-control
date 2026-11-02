package browser

import (
	"context"
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
)

type blockedPairWriter struct {
	header      http.Header
	entered     chan struct{}
	interrupted chan struct{}
	once        sync.Once
}

func (w *blockedPairWriter) Header() http.Header { return w.header }
func (w *blockedPairWriter) WriteHeader(int)     {}
func (w *blockedPairWriter) Write([]byte) (int, error) {
	close(w.entered)
	<-w.interrupted
	return 0, errors.New("write cancelled")
}
func (w *blockedPairWriter) SetReadDeadline(time.Time) error { return nil }
func (w *blockedPairWriter) SetWriteDeadline(value time.Time) error {
	if !value.After(time.Now()) {
		w.once.Do(func() { close(w.interrupted) })
	}
	return nil
}
func (w *blockedPairWriter) FlushError() error { return nil }

func TestPairRevocationCancelsBlockedResponse(t *testing.T) {
	s, code, err := newSession("127.0.0.1:19080", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:19080/api/session/pair", strings.NewReader(`{"code":"`+code+`"}`))
	r.Header.Set("Origin", "http://127.0.0.1:19080")
	r.Header.Set("Content-Type", "application/json")
	w := &blockedPairWriter{header: make(http.Header), entered: make(chan struct{}), interrupted: make(chan struct{})}
	handled := make(chan struct{})
	go func() { s.pairHTTP(w, r); close(handled) }()
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("pair response did not start")
	}
	revoked := make(chan struct{})
	go func() { s.revoke(); close(revoked) }()
	select {
	case <-revoked:
	case <-time.After(time.Second):
		t.Fatal("revoke waited for blocked response")
	}
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("handler did not stop")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cookie != "" || s.proof != "" {
		t.Fatal("failed pairing response left session usable")
	}
}

func TestPairLimitsConcurrentIncompleteRequests(t *testing.T) {
	var handler http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	defer server.Close()
	s, code, err := newSession(server.Listener.Addr().String(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer s.revoke()
	fake := &routeService{}
	handler = newBoundRoutes(s, fake, fake)
	server.Start()
	for range 2 {
		conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if _, err := fmt.Fprintf(conn, "POST /api/session/pair HTTP/1.1\r\nHost: %s\r\nOrigin: http://%s\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\nx", s.host, s.host); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for len(s.pairing) != 2 {
		select {
		case <-ctx.Done():
			t.Fatal("incomplete requests not admitted")
		case <-time.After(time.Millisecond):
		}
	}
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/session/pair", strings.NewReader(`{"code":"`+code+`"}`))
	r.Header.Set("Origin", server.URL)
	r.Header.Set("Content-Type", "application/json")
	resp, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("pairing limit: %d", resp.StatusCode)
	}
}

func TestBrowserPairRouteRequiresOriginAndOneTimeCode(t *testing.T) {
	var handler http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	defer server.Close()
	s, code, err := newSession(server.Listener.Addr().String(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer s.revoke()
	fake := &routeService{}
	handler = newBoundRoutes(s, fake, fake)
	server.Start()
	request := func(origin, body string) (*http.Response, []byte) {
		t.Helper()
		r, err := http.NewRequest(http.MethodPost, server.URL+"/api/session/pair", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Origin", origin)
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
		return resp, data
	}
	valid := `{"code":"` + code + `"}`
	if resp, _ := request("http://evil.invalid", valid); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}
	if resp, _ := request(server.URL, `{"code":"wrong","code":"`+code+`"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("ambiguous code: %d", resp.StatusCode)
	}
	if resp, _ := request(server.URL, `{"code":"wrong"}`); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong code: %d", resp.StatusCode)
	}
	resp, data := request(server.URL, valid)
	var result struct {
		Proof string `json:"proof"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &result) != nil || result.Proof == "" {
		t.Fatalf("pair status %d", resp.StatusCode)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("cookie missing required protection")
	}
	if strings.Contains(string(data), code) || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("pair response contains code or is cacheable")
	}
	if resp, _ := request(server.URL, valid); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused code: %d", resp.StatusCode)
	}
}
