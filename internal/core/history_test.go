package core

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/demo"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

type blockedHistory struct {
	*storage.Store
	entered chan struct{}
	release chan struct{}
}

type blockedResponse struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release chan struct{}
}

type gatedListener struct {
	net.Listener
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (l *gatedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &gatedConnection{Conn: conn, listener: l}, nil
}

type gatedConnection struct {
	net.Conn
	listener *gatedListener
}

func (c *gatedConnection) Write(p []byte) (int, error) {
	c.listener.once.Do(func() { close(c.listener.entered); <-c.listener.release })
	return c.Conn.Write(p)
}

func TestHistoryFlushesBeforeWorkerFinishes(t *testing.T) {
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
	owner, err := leases.acquire(context.Background(), uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := &coreHTTP{secret: "secret", leases: leases, history: store}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gate := &gatedListener{Listener: listener, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(gate.release) })
	handlerDone := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		close(handlerDone)
	}), WriteTimeout: 5 * time.Second}
	defer func() { _ = server.Close() }()
	go func() { _ = server.Serve(gate) }()
	req, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/v1/tests", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set(leaseHeader, owner.id.String())
	clientDone := make(chan error, 1)
	go func() {
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err == nil {
			err = response.Body.Close()
		}
		clientDone <- err
	}()
	select {
	case <-gate.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("network write did not start")
	}
	select {
	case <-handlerDone:
		t.Fatal("handler returned before network flush")
	default:
	}
	workers := handler.stopWorkers()
	select {
	case <-workers:
		t.Fatal("flush was not tracked")
	default:
	}
	release.Do(func() { close(gate.release) })
	select {
	case err := <-clientDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client did not finish")
	}
	select {
	case <-workers:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not finish")
	}
}

func (w *blockedResponse) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return w.ResponseRecorder.Write(p)
}

func TestHistoryWorkerTracksResponseDelivery(t *testing.T) {
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
	owner, err := leases.acquire(context.Background(), uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := &coreHTTP{secret: "secret", leases: leases, history: store}
	req := httptest.NewRequest(http.MethodGet, "/v1/tests", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set(leaseHeader, owner.id.String())
	response := &blockedResponse{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, req); close(done) }()
	select {
	case <-response.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("response did not start")
	}
	workers := handler.stopWorkers()
	select {
	case <-workers:
		t.Fatal("response delivery was not tracked")
	default:
	}
	close(response.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("response did not finish")
	}
	select {
	case <-workers:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not finish")
	}
}

func TestHistoryMethodResponses(t *testing.T) {
	handler := &coreHTTP{secret: "secret"}
	for _, tc := range []struct{ method, path, allow string }{
		{http.MethodPut, "/v1/tests", "GET, POST"},
		{http.MethodPost, "/v1/tests/" + uuid.NewString() + "/audit", "GET"},
		{http.MethodPut, "/v1/tests/" + uuid.NewString() + "/measurements", "GET"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != tc.allow {
			t.Fatalf("%s %s: %d allow %q", tc.method, tc.path, response.Code, response.Header().Get("Allow"))
		}
	}
}

func (s *blockedHistory) ListTests(ctx context.Context, after *uuid.UUID, limit int) ([]contract.TestRecord, *uuid.UUID, error) {
	close(s.entered)
	<-s.release // Deliberately ignores cancellation to test worker ownership.
	return s.Store.ListTests(context.Background(), after, limit)
}

func TestHistoryRevocationAndBlockedReadRemainBounded(t *testing.T) {
	for _, revoke := range []bool{true, false} {
		t.Run(map[bool]string{true: "revoked", false: "deadline"}[revoke], func(t *testing.T) {
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
			owner, err := leases.acquire(context.Background(), uuid.New(), uuid.Nil)
			if err != nil {
				t.Fatal(err)
			}
			reader := &blockedHistory{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
			handler := &coreHTTP{secret: "secret", leases: leases, history: reader}
			req := httptest.NewRequest(http.MethodGet, "/v1/tests", nil)
			req.Header.Set("Authorization", "Bearer secret")
			req.Header.Set(leaseHeader, owner.id.String())
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { response := httptest.NewRecorder(); handler.ServeHTTP(response, req); done <- response }()
			select {
			case <-reader.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("read did not start")
			}
			if revoke {
				if err := leases.release(owner.id); err != nil {
					t.Fatal(err)
				}
				if _, err := leases.acquire(context.Background(), uuid.New(), uuid.Nil); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case response := <-done:
				want := http.StatusServiceUnavailable
				if revoke {
					want = http.StatusForbidden
				}
				if response.Code != want {
					t.Fatalf("response: %d, want %d", response.Code, want)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("request exceeded bound")
			}
			workers := handler.stopWorkers()
			select {
			case <-workers:
				t.Fatal("blocked reader was not tracked")
			default:
			}
			close(reader.release)
			select {
			case <-workers:
			case <-time.After(5 * time.Second):
				t.Fatal("reader did not finish")
			}
		})
	}
}
