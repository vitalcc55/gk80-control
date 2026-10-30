package core

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/demo"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

func TestStreamRejectsOversizedMessageBeforeWrite(t *testing.T) {
	if err := writeStream(context.Background(), nil, strings.Repeat("x", contract.MaxEventBytes)); err == nil {
		t.Fatal("oversized stream message accepted")
	}
}

type streamGateListener struct {
	net.Listener
	gate *streamGate
}

func (l *streamGateListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &streamGateConn{Conn: conn, gate: l.gate, closed: make(chan struct{})}, nil
}

type streamGate struct {
	block   atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type streamGateConn struct {
	net.Conn
	gate   *streamGate
	closed chan struct{}
	once   sync.Once
}

func (c *streamGateConn) Write(p []byte) (int, error) {
	if c.gate.block.Load() {
		c.gate.once.Do(func() { close(c.gate.entered) })
		select {
		case <-c.gate.release:
		case <-c.closed:
			return 0, net.ErrClosed
		}
	}
	return c.Conn.Write(p)
}

func (c *streamGateConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func TestStreamOverflowClosesBlockedWrite(t *testing.T) {
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
	owner, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	source := &eventTestSource{state: demo.State{BackendRunID: uuid.New()}, changed: make(chan struct{}, 1)}
	feed := newEventFeed(source)
	streams := newStreamGroup()
	handler := &coreHTTP{secret: "secret", leases: leases, feed: feed, streams: streams}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gate := &streamGate{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(gate.release)
	server := &http.Server{Handler: handler, WriteTimeout: 5 * time.Second}
	defer func() { _ = server.Close() }()
	go func() { _ = server.Serve(&streamGateListener{Listener: listener, gate: gate}) }()
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer secret")
	headers.Set(leaseHeader, owner.id.String())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "http://"+listener.Addr().String()+"/v1/events", &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	feed.mu.Lock()
	sub := feed.current
	feed.mu.Unlock()
	if sub == nil {
		t.Fatal("missing subscription")
	}
	gate.block.Store(true)
	closeDeadline := time.Now().Add(time.Second)
	feed.publish()
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("stream write did not block")
	}
	for i := 0; i <= eventQueueLimit; i++ {
		feed.publish()
	}
	select {
	case <-sub.done:
	default:
		t.Fatal("queue did not overflow")
	}
	closeCtx, closeCancel := context.WithDeadline(ctx, closeDeadline)
	defer closeCancel()
	if _, _, err := conn.Read(closeCtx); err == nil || closeCtx.Err() != nil {
		t.Fatal("overflow left stream open")
	}
	select {
	case <-streams.stop():
	case <-ctx.Done():
		t.Fatal("stream did not drain")
	}
}

func TestStreamRejectsUnexpectedDataWithoutWaitingForHandshake(t *testing.T) {
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
	owner, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	feed := newEventFeed(engine)
	streams := newStreamGroup()
	server := httptest.NewServer(&coreHTTP{secret: "secret", leases: leases, feed: feed, streams: streams})
	defer server.Close()
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer secret")
	headers.Set(leaseHeader, owner.id.String())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, server.URL+"/v1/events", &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	feed.mu.Lock()
	sub := feed.current
	feed.mu.Unlock()
	if sub == nil {
		t.Fatal("missing subscription")
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte("unexpected")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sub.done:
	case <-ctx.Done():
		t.Fatal("data frame did not close subscription")
	}
	select {
	case <-streams.stop():
	case <-ctx.Done():
		t.Fatal("data frame delayed stream shutdown")
	}
}

func TestRevokedOwnerCannotReplaceCurrentSubscription(t *testing.T) {
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
	old, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	feed := newEventFeed(engine)
	if _, err := leases.subscribe(old, feed); err != nil {
		t.Fatal(err)
	}
	if err := leases.release(old.id); err != nil {
		t.Fatal(err)
	}
	current, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := leases.subscribe(current, feed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leases.subscribe(old, feed); !errors.Is(err, errLeaseInvalid) {
		t.Fatalf("old owner: %v", err)
	}
	select {
	case <-sub.done:
		t.Fatal("old owner detached current subscription")
	default:
	}
	feed.unsubscribe(sub)
}

func TestStreamGroupBoundsAndDrainsConnections(t *testing.T) {
	group := newStreamGroup()
	first := group.begin()
	second := group.begin()
	third := group.begin()
	if !first || !second || third {
		t.Fatal("stream limit not enforced")
	}
	done := group.stop()
	if group.begin() {
		t.Fatal("stream admitted during shutdown")
	}
	select {
	case <-done:
		t.Fatal("streams did not drain")
	default:
	}
	group.end()
	select {
	case <-done:
		t.Fatal("one stream remains")
	default:
	}
	group.end()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("streams did not finish")
	}
}
