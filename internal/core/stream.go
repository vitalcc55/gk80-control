package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

// A replacement may be upgrading while the previous stream is closing.
type streamGroup struct {
	mu       sync.Mutex
	count    int
	closing  bool
	stopping chan struct{}
	done     chan struct{}
}

func newStreamGroup() *streamGroup {
	return &streamGroup{stopping: make(chan struct{}), done: make(chan struct{})}
}

func (g *streamGroup) begin() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closing || g.count >= 2 {
		return false
	}
	g.count++
	return true
}

func (g *streamGroup) end() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.count--
	if g.closing && g.count == 0 {
		close(g.done)
	}
}

func (g *streamGroup) stop() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.closing {
		g.closing = true
		close(g.stopping)
		if g.count == 0 {
			close(g.done)
		}
	}
	return g.done
}

// websocket.Accept owns handshake validation; adapt its failures to ApiError.
type streamResponse struct {
	http.ResponseWriter
	failed bool
}

func (w *streamResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *streamResponse) WriteHeader(status int) {
	if status == http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.failed = true
	writeAPIError(w.ResponseWriter, status, "invalid_upgrade", "WebSocket upgrade rejected")
}
func (w *streamResponse) Write(body []byte) (int, error) {
	if w.failed {
		return len(body), nil
	}
	return w.ResponseWriter.Write(body)
}

func (h *coreHTTP) events(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	owner, ok := h.requestOwner(w, r)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
		return
	}
	if !h.streams.begin() {
		writeAPIError(w, http.StatusServiceUnavailable, "state_unavailable", "State is not yet confirmed")
		return
	}
	defer h.streams.end()
	conn, err := websocket.Accept(&streamResponse{ResponseWriter: w}, r, nil)
	if err != nil {
		return
	}
	var readerDone <-chan struct{}
	defer func() {
		_ = conn.CloseNow()
		if readerDone != nil {
			<-readerDone
		}
	}()
	select {
	case <-h.streams.stopping:
		return
	default:
	}
	sub, err := h.leases.subscribe(owner, h.feed)
	if err != nil {
		return
	}
	defer h.feed.unsubscribe(sub)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finishedReading := make(chan struct{})
	readerDone = finishedReading
	go func() {
		defer close(finishedReading)
		_, _, _ = conn.Reader(ctx)
		cancel()
	}()
	go func() {
		select {
		case <-owner.done:
		case <-sub.done:
		case <-h.streams.stopping:
		case <-ctx.Done():
		}
		cancel()
	}()
	state := h.feed.snapshot().state
	if !h.leases.valid(owner) || ctx.Err() != nil {
		return
	}
	if err := writeStream(ctx, conn, contract.StreamReady{Type: "ready", BackendRunId: state.BackendRunID}); err != nil {
		return
	}
	for {
		event, err := h.feed.next(ctx, sub)
		if err != nil {
			return
		}
		if !h.leases.valid(owner) || ctx.Err() != nil {
			return
		}
		if err := writeStream(ctx, conn, event); err != nil {
			return
		}
	}
}

func writeStream(ctx context.Context, conn *websocket.Conn, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(body) > contract.MaxEventBytes {
		return errors.New("core event exceeds size limit")
	}
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, body)
}
