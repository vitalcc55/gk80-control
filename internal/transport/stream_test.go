package transport

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
)

type streamFixture struct {
	client          *Client
	runID           uuid.UUID
	snapshotRunID   uuid.UUID
	send            chan contract.Event
	raw             chan []byte
	written         chan struct{}
	snapshotEntered chan struct{}
	snapshotRelease chan struct{}
	stop            chan struct{}
	stopOnce        sync.Once
	server          *httptest.Server
}

type gatedContext struct {
	context.Context
	checked chan struct{}
	release chan struct{}
	checks  atomic.Int32
}

func (c *gatedContext) Err() error {
	if c.checks.Add(1) == 1 {
		close(c.checked)
		<-c.release
	}
	return c.Context.Err()
}

func newStreamFixture(t *testing.T) *streamFixture {
	t.Helper()
	f := &streamFixture{runID: uuid.New(), send: make(chan contract.Event), raw: make(chan []byte), written: make(chan struct{}), snapshotEntered: make(chan struct{}), snapshotRelease: make(chan struct{}), stop: make(chan struct{})}
	f.snapshotRunID = f.runID
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/events":
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			body, _ := json.Marshal(contract.StreamReady{Type: "ready", BackendRunId: f.runID})
			if err := conn.Write(r.Context(), websocket.MessageText, body); err != nil {
				return
			}
			for {
				select {
				case event := <-f.send:
					body, _ := json.Marshal(event)
					if err := conn.Write(r.Context(), websocket.MessageText, body); err != nil {
						return
					}
					select {
					case f.written <- struct{}{}:
					case <-f.stop:
						return
					case <-r.Context().Done():
						return
					}
				case body := <-f.raw:
					if err := conn.Write(r.Context(), websocket.MessageText, body); err != nil {
						return
					}
					select {
					case f.written <- struct{}{}:
					case <-f.stop:
						return
					case <-r.Context().Done():
						return
					}
				case <-f.stop:
					return
				case <-r.Context().Done():
					return
				}
			}
		case "/v1/snapshot":
			close(f.snapshotEntered)
			select {
			case <-f.snapshotRelease:
			case <-r.Context().Done():
				return
			}
			_ = json.NewEncoder(w).Encode(contract.Snapshot{ApiVersion: "v1", BackendRunId: f.snapshotRunID, LastEventSequence: 1, Measurements: []contract.Measurement{}})
		default:
			http.NotFound(w, r)
		}
	}))
	f.client = &Client{endpoint: f.server.URL, secret: "secret", http: f.server.Client()}
	t.Cleanup(func() { f.stopOnce.Do(func() { close(f.stop) }); f.server.Close() })
	return f
}

func (f *streamFixture) sendEvent(t *testing.T, ctx context.Context, event contract.Event) {
	t.Helper()
	select {
	case f.send <- event:
	case <-ctx.Done():
		t.Fatal("event send blocked")
	}
	select {
	case <-f.written:
	case <-ctx.Done():
		t.Fatal("event not sent")
	}
}

func TestNativeStreamProbeDetectsSilentPeer(t *testing.T) {
	f := newStreamFixture(t)
	close(f.snapshotRelease)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, _, err := f.client.OpenStream(ctx, uuid.New(), f.runID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close(context.Background()) }()
	result := make(chan error, 1)
	go func() { result <- stream.Probe(context.Background()) }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("silent peer was reported live")
		}
	case <-ctx.Done():
		t.Fatal("probe exceeded its own deadline")
	}
	select {
	case <-stream.Done():
	case <-ctx.Done():
		t.Fatal("unconfirmed stream remained usable")
	}
}

func TestNativeStreamWaitsForInitialSnapshot(t *testing.T) {
	run := uuid.New()
	stop := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/events":
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			body, _ := json.Marshal(contract.StreamReady{Type: "ready", BackendRunId: run})
			if err := conn.Write(r.Context(), websocket.MessageText, body); err != nil {
				return
			}
			<-stop
		case "/v1/snapshot":
			if requests.Add(1) <= 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"code":"state_unavailable","message":"State is not yet confirmed"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(contract.Snapshot{ApiVersion: "v1", BackendRunId: run, Measurements: []contract.Measurement{}})
		}
	}))
	defer server.Close()
	defer close(stop)
	client := &Client{endpoint: server.URL, secret: "secret", http: server.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, snapshot, err := client.OpenStream(ctx, uuid.New(), run)
	if err != nil || snapshot.BackendRunId != run || requests.Load() != 3 {
		t.Fatalf("initial snapshot: %+v requests=%d err=%v", snapshot, requests.Load(), err)
	}
	if err := stream.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestNativeStreamProbeCancellationInvalidatesStream(t *testing.T) {
	f := newStreamFixture(t)
	close(f.snapshotRelease)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, _, err := f.client.OpenStream(ctx, uuid.New(), f.runID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close(context.Background()) }()
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if err := stream.Probe(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled probe: %v", err)
	}
	select {
	case <-stream.Done():
	case <-ctx.Done():
		t.Fatal("cancelled probe did not invalidate stream")
	}
}

func TestNativeStreamCloseInterruptsProbe(t *testing.T) {
	f := newStreamFixture(t)
	close(f.snapshotRelease)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, _, err := f.client.OpenStream(ctx, uuid.New(), f.runID)
	if err != nil {
		t.Fatal(err)
	}
	// The peer never reads, so a probe cannot receive a pong.
	result := make(chan error, 1)
	go func() { result <- stream.Probe(ctx) }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for stream.probeMu.TryLock() {
		stream.probeMu.Unlock()
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("probe did not start")
		}
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if err := stream.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("probe succeeded after close")
		}
	case <-ctx.Done():
		t.Fatal("probe did not stop after close")
	}
}

func TestNativeStreamSnapshotBoundaryAndGap(t *testing.T) {
	f := newStreamFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	type result struct {
		stream   *EventStream
		snapshot contract.Snapshot
		err      error
	}
	opened := make(chan result, 1)
	go func() {
		stream, snapshot, err := f.client.OpenStream(ctx, uuid.New(), f.runID)
		opened <- result{stream, snapshot, err}
	}()
	select {
	case <-f.snapshotEntered:
	case <-ctx.Done():
		t.Fatal("snapshot did not start")
	}
	for _, sequence := range []int64{1, 2} {
		f.sendEvent(t, ctx, contract.Event{Type: "stateChanged", BackendRunId: f.runID, Sequence: sequence, Revision: 1})
	}
	close(f.snapshotRelease)
	var out result
	select {
	case out = <-opened:
	case <-ctx.Done():
		t.Fatal("stream did not open")
	}
	if out.err != nil || out.snapshot.LastEventSequence != 1 {
		t.Fatalf("open: %+v %v", out.snapshot, out.err)
	}
	defer func() {
		closeCtx, done := context.WithTimeout(context.Background(), 4*time.Second)
		defer done()
		_ = out.stream.Close(closeCtx)
	}()
	for len(out.stream.queue) < 2 {
		select {
		case <-ctx.Done():
			t.Fatal("events did not reach the client queue")
		case <-time.After(time.Millisecond):
		}
	}
	event, present, err := out.stream.Poll(ctx)
	if err != nil || !present || event.Sequence != 2 {
		t.Fatalf("boundary: %+v %v %v", event, present, err)
	}
	if _, present, err := out.stream.Poll(ctx); err != nil || present {
		t.Fatalf("buffer was not drained: %v %v", present, err)
	}
	f.sendEvent(t, ctx, contract.Event{Type: "stateChanged", BackendRunId: f.runID, Sequence: 4, Revision: 1})
	if _, err := out.stream.Next(ctx); !errors.Is(err, ErrStreamSequence) {
		t.Fatalf("gap: %v", err)
	}
}

func TestNativeStreamDisconnectDuringSnapshotCannotOpen(t *testing.T) {
	f := newStreamFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := f.client.OpenStream(ctx, uuid.New(), f.runID); done <- err }()
	select {
	case <-f.snapshotEntered:
	case <-ctx.Done():
		t.Fatal("snapshot did not start")
	}
	f.stopOnce.Do(func() { close(f.stop) })
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("disconnected stream opened")
		}
	case <-ctx.Done():
		t.Fatal("disconnect did not cancel snapshot")
	}
}

func TestNativeStreamOverflowDuringSnapshot(t *testing.T) {
	f := newStreamFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := f.client.OpenStream(ctx, uuid.New(), f.runID); done <- err }()
	select {
	case <-f.snapshotEntered:
	case <-ctx.Done():
		t.Fatal("snapshot did not start")
	}
	for sequence := int64(1); sequence <= nativeEventQueueLimit+1; sequence++ {
		select {
		case f.send <- contract.Event{Type: "stateChanged", BackendRunId: f.runID, Sequence: sequence, Revision: 1}:
		case <-ctx.Done():
			t.Fatal("event send blocked")
		}
		select {
		case <-f.written:
		case <-ctx.Done():
			t.Fatal("event not sent")
		}
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrStreamOverflow) {
			t.Fatalf("overflow: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("overflow did not cancel snapshot")
	}
}

func TestNativeStreamRejectsChangedRunID(t *testing.T) {
	f := newStreamFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	type result struct {
		stream *EventStream
		err    error
	}
	opened := make(chan result, 1)
	go func() { stream, _, err := f.client.OpenStream(ctx, uuid.New(), f.runID); opened <- result{stream, err} }()
	select {
	case <-f.snapshotEntered:
	case <-ctx.Done():
		t.Fatal("snapshot did not start")
	}
	close(f.snapshotRelease)
	var out result
	select {
	case out = <-opened:
	case <-ctx.Done():
		t.Fatal("stream did not open")
	}
	if out.err != nil {
		t.Fatal(out.err)
	}
	defer func() {
		closeCtx, done := context.WithTimeout(context.Background(), 4*time.Second)
		defer done()
		_ = out.stream.Close(closeCtx)
	}()
	f.sendEvent(t, ctx, contract.Event{Type: "stateChanged", BackendRunId: uuid.New(), Sequence: 2, Revision: 1})
	if _, err := out.stream.Next(ctx); !errors.Is(err, ErrStreamInvalid) {
		t.Fatalf("changed run: %v", err)
	}
}

func TestNativeStreamRejectsSnapshotFromAnotherRun(t *testing.T) {
	f := newStreamFixture(t)
	f.snapshotRunID = uuid.New()
	close(f.snapshotRelease)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	stream, _, err := f.client.OpenStream(ctx, uuid.New(), f.runID)
	if stream != nil || !errors.Is(err, ErrStreamInvalid) {
		t.Fatalf("snapshot identity: %v", err)
	}
}

func TestNativeStreamRejectsMalformedAndOversizedFrames(t *testing.T) {
	for _, malformed := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing-field", false: "size-limit"}[malformed], func(t *testing.T) {
			f := newStreamFixture(t)
			close(f.snapshotRelease)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			stream, _, err := f.client.OpenStream(ctx, uuid.New(), f.runID)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				closeCtx, done := context.WithTimeout(context.Background(), 4*time.Second)
				defer done()
				_ = stream.Close(closeCtx)
			}()
			valid, err := json.Marshal(contract.Event{Type: "stateChanged", BackendRunId: f.runID, Sequence: 2, Revision: 1})
			if err != nil {
				t.Fatal(err)
			}
			if !malformed {
				valid = append(valid, []byte(strings.Repeat(" ", contract.MaxEventBytes-len(valid)))...)
				select {
				case f.raw <- valid:
				case <-ctx.Done():
					t.Fatal("frame send blocked")
				}
				select {
				case <-f.written:
				case <-ctx.Done():
					t.Fatal("frame not sent")
				}
				if event, err := stream.Next(ctx); err != nil || event.Sequence != 2 {
					t.Fatalf("boundary frame: %+v %v", event, err)
				}
				valid = append(valid, ' ')
			} else {
				valid = []byte(`{"type":"stateChanged","backendRunId":"` + f.runID.String() + `","sequence":2}`)
			}
			select {
			case f.raw <- valid:
			case <-ctx.Done():
				t.Fatal("invalid frame send blocked")
			}
			if _, err := stream.Next(ctx); !errors.Is(err, ErrStreamInvalid) || ctx.Err() != nil {
				t.Fatalf("invalid frame accepted or timed out: %v", err)
			}
		})
	}
}

func TestNativeStreamCancellationDoesNotLoseQueuedEvent(t *testing.T) {
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	stream := &EventStream{ctx: lifetime, cancel: stop, queue: make(chan contract.Event, 1), done: make(chan struct{}), consumer: make(chan struct{}, 1)}
	stream.queue <- contract.Event{Sequence: 1}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := stream.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
	event, err := stream.Next(context.Background())
	if err != nil || event.Sequence != 1 {
		t.Fatalf("queued event lost: %+v %v", event, err)
	}
}

func TestNativeStreamPendingIncludesDequeuedEvent(t *testing.T) {
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	stream := &EventStream{ctx: lifetime, cancel: stop, queue: make(chan contract.Event, 1), done: make(chan struct{}), consumer: make(chan struct{}, 1)}
	stream.mu.Lock()
	stream.queue <- contract.Event{Sequence: 1}
	event := make(chan contract.Event, 1)
	go func() { value, _ := stream.Next(context.Background()); event <- value }()
	pending := make(chan bool, 1)
	go func() { pending <- stream.Pending() }()
	stream.mu.Unlock()
	if !<-pending || (<-event).Sequence != 1 {
		t.Fatal("dequeued event was not marked pending")
	}
	if !stream.Pending() {
		t.Fatal("event was cleared before a snapshot")
	}
}

func TestNativeStreamCancellationBeforeDequeue(t *testing.T) {
	for _, cancelLifetime := range []bool{false, true} {
		t.Run(map[bool]string{false: "request", true: "lifetime"}[cancelLifetime], func(t *testing.T) {
			lifetime, stop := context.WithCancel(context.Background())
			defer stop()
			stream := &EventStream{ctx: lifetime, cancel: stop, queue: make(chan contract.Event, 1), done: make(chan struct{}), consumer: make(chan struct{}, 1)}
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := &gatedContext{Context: base, checked: make(chan struct{}), release: make(chan struct{})}
			type result struct {
				event contract.Event
				err   error
			}
			done := make(chan result, 1)
			go func() { event, err := stream.Next(request); done <- result{event, err} }()
			select {
			case <-request.checked:
			case <-time.After(3 * time.Second):
				t.Fatal("read did not start")
			}
			stream.mu.Lock()
			close(request.release)
			stream.queue <- contract.Event{Sequence: 1}
			if cancelLifetime {
				stop()
			} else {
				cancel()
			}
			stream.mu.Unlock()
			select {
			case result := <-done:
				if cancelLifetime && !errors.Is(result.err, ErrStreamClosed) {
					t.Fatalf("lifetime cancellation: %v", result.err)
				}
				if !cancelLifetime && !errors.Is(result.err, context.Canceled) {
					t.Fatalf("request cancellation: %+v %v", result.event, result.err)
				}
				if !cancelLifetime {
					event, err := stream.Next(context.Background())
					if err != nil || event.Sequence != 1 {
						t.Fatalf("event lost before dequeue: %+v %v", event, err)
					}
				}
			case <-time.After(3 * time.Second):
				t.Fatal("read did not finish")
			}
		})
	}
}

func TestNativeStreamKeepsTerminalCause(t *testing.T) {
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	stream := &EventStream{ctx: lifetime, cancel: stop, queue: make(chan contract.Event, 1), done: make(chan struct{}), consumer: make(chan struct{}, 1)}
	_ = stream.fail(ErrStreamOverflow)
	for i := 0; i < 100; i++ {
		if _, err := stream.Next(context.Background()); !errors.Is(err, ErrStreamOverflow) {
			t.Fatalf("terminal cause: %v", err)
		}
	}
}

func TestNativeStreamCloseReportsUnconfirmedReader(t *testing.T) {
	lifetime, stop := context.WithCancel(context.Background())
	readerDone, watchDone := make(chan struct{}), make(chan struct{})
	close(watchDone)
	stream := &EventStream{ctx: lifetime, cancel: stop, done: make(chan struct{}), readerDone: readerDone, watchDone: watchDone}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := stream.Close(ctx); !errors.Is(err, ErrStreamCloseUnconfirmed) {
		t.Fatalf("close: %v", err)
	}
	close(readerDone)
}

func TestNativeStreamCancellationClosesWithoutConsumer(t *testing.T) {
	f := newStreamFixture(t)
	close(f.snapshotRelease)
	lifetime, stop := context.WithCancel(context.Background())
	stream, _, err := f.client.OpenStream(lifetime, uuid.New(), f.runID)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	stop()
	select {
	case <-stream.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not close Done")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := stream.Close(ctx); err != nil {
		t.Fatalf("stream cleanup: %v", err)
	}
}

func TestNativeStreamDistinguishesEarlyCloseAndInvalidReady(t *testing.T) {
	for _, kind := range []string{"closed", "invalid", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				if kind == "invalid" {
					_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"ready"}`))
				}
				if kind == "oversized" {
					_ = conn.Write(r.Context(), websocket.MessageText, []byte(strings.Repeat(" ", contract.MaxEventBytes+1)))
				}
			}))
			defer server.Close()
			client := &Client{endpoint: server.URL, secret: "secret", http: server.Client()}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _, err := client.OpenStream(ctx, uuid.New(), uuid.New())
			want := ErrStreamClosed
			if kind != "closed" {
				want = ErrStreamInvalid
			}
			if !errors.Is(err, want) {
				t.Fatalf("ready: %v", err)
			}
		})
	}
}

func TestNativeStreamLifetimeCancellationWakesNext(t *testing.T) {
	lifetime, stop := context.WithCancel(context.Background())
	stream := &EventStream{ctx: lifetime, cancel: stop, queue: make(chan contract.Event, 1), done: make(chan struct{}), consumer: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() { _, err := stream.Next(context.Background()); done <- err }()
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, ErrStreamClosed) {
			t.Fatalf("next: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("next did not wake")
	}
	select {
	case <-stream.Done():
	default:
		t.Fatal("stream was not marked closed")
	}
}

func TestNativeStreamCancellationDuringSnapshotMarksClosed(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer server.Close()
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	client := &Client{endpoint: server.URL, secret: "secret", http: server.Client()}
	stream := &EventStream{client: client, lease: uuid.New(), ctx: lifetime, cancel: stop, queue: make(chan contract.Event, 1), done: make(chan struct{}), consumer: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() { _, err := stream.Snapshot(context.Background()); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("snapshot did not start")
	}
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, ErrStreamClosed) {
			t.Fatalf("snapshot cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("snapshot did not stop")
	}
	select {
	case <-stream.Done():
	default:
		t.Fatal("stream was not marked closed")
	}
}

func TestNativeStreamLifetimeCancellationPreventsResults(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		lifetime, stop := context.WithCancel(context.Background())
		stream := &EventStream{ctx: lifetime, cancel: stop, queue: make(chan contract.Event, 1), done: make(chan struct{}), consumer: make(chan struct{}, 1)}
		stream.queue <- contract.Event{Sequence: 1}
		stop()
		var err error
		if snapshot {
			_, err = stream.Snapshot(context.Background())
		} else {
			_, err = stream.Next(context.Background())
		}
		if !errors.Is(err, ErrStreamClosed) {
			t.Fatalf("cancelled lifetime: %v", err)
		}
		select {
		case <-stream.Done():
		default:
			t.Fatal("cancelled lifetime not marked closed")
		}
	}
}

func TestNativeStreamConsumerWaitHonorsContext(t *testing.T) {
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	stream := &EventStream{ctx: lifetime, cancel: stop, queue: make(chan contract.Event, 1), done: make(chan struct{}), consumer: make(chan struct{}, 1)}
	stream.consumer <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := stream.Snapshot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("consumer wait: %v", err)
	}
}

func TestNativeStreamPinsTLSAndRejectsRedirect(t *testing.T) {
	var received atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Store(true) }))
	defer server.Close()
	client, err := NewClient(server.Listener.Addr().String(), sha256.Sum256([]byte("wrong certificate")), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.OpenStream(context.Background(), uuid.New(), uuid.New()); err == nil || received.Load() {
		t.Fatal("untrusted server received credentials")
	}
	var redirected atomic.Bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Store(true) }))
	defer target.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	client, err = NewClient(redirect.Listener.Addr().String(), sha256.Sum256(redirect.Certificate().Raw), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.OpenStream(context.Background(), uuid.New(), uuid.New()); err == nil || redirected.Load() {
		t.Fatal("redirect reached another endpoint")
	}
}

func TestNativeStreamReceivesCoreEvents(t *testing.T) {
	executable := buildCore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	process, handshake, err := Start(ctx, executable, t.TempDir(), "test", "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close(context.Background()) })
	client := process.client
	lease, err := client.AcquireLease(ctx, uuid.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	stream, snapshot, err := client.OpenStream(ctx, lease.LeaseId, handshake.BackendRunId)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Probe(ctx); err != nil {
		t.Fatalf("core stream probe: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer closeCancel()
		_ = stream.Close(closeCtx)
	})
	command := contract.CreateTestCommand{CommandId: uuid.New(), ExpectedRevision: snapshot.Revision, Parameters: contract.TestParameters{Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 10, Direction: "pressure"}}
	result, err := client.CreateTest(ctx, lease.LeaseId, command)
	if err != nil || result.TestId == nil {
		t.Fatalf("create: %+v %v", result, err)
	}
	event, err := stream.Next(ctx)
	if err != nil || event.BackendRunId != handshake.BackendRunId || event.Sequence != snapshot.LastEventSequence+1 {
		t.Fatalf("event: %+v %v", event, err)
	}
	updated, err := stream.Snapshot(ctx)
	if err != nil || updated.LastEventSequence < event.Sequence || updated.ActiveTest == nil || updated.ActiveTest.TestId != *result.TestId {
		t.Fatalf("updated snapshot: %+v %v", updated, err)
	}
	ready, err := client.Readiness(ctx, lease.LeaseId, *result.TestId)
	if err != nil || !ready.Ready {
		t.Fatalf("readiness: %+v %v", ready, err)
	}
	started, err := client.StartTest(ctx, lease.LeaseId, *result.TestId, contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: ready.Revision, ValveClosed: true, ConnectionConfirmed: true})
	if err != nil || started.Status != "accepted" {
		t.Fatalf("start: %+v %v", started, err)
	}
	for updated.PendingAction == nil {
		if _, err := stream.Next(ctx); err != nil {
			t.Fatal(err)
		}
		updated, err = stream.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	confirmed, err := client.ConfirmAction(ctx, lease.LeaseId, *result.TestId, updated.PendingAction.ActionId, contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: updated.Revision})
	if err != nil || confirmed.Status != "completed" {
		t.Fatalf("confirm: %+v %v", confirmed, err)
	}
	updated, err = stream.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	measurementRevision := updated.Revision
	var firstSequence int64
	for _, measurement := range updated.Measurements {
		if measurement.Channel == "dd1" {
			firstSequence = measurement.Sequence
		}
	}
	for {
		event, err = stream.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		updated, err = stream.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		advanced := false
		for _, measurement := range updated.Measurements {
			if measurement.Channel != "dd1" {
				continue
			}
			if firstSequence == 0 {
				firstSequence = measurement.Sequence
				continue
			}
			if measurement.Sequence > firstSequence && event.Revision == measurementRevision && updated.Revision == measurementRevision {
				advanced = true
			}
		}
		if advanced {
			break
		}
	}
	if err := client.ReleaseLease(ctx, lease.LeaseId); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := stream.Next(ctx); err != nil {
			if ctx.Err() != nil || !errors.Is(err, ErrStreamClosed) {
				t.Fatalf("revoked stream did not close: %v", err)
			}
			break
		}
	}
}
