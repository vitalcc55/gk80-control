package browser

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

const maxResponseBytes = 4 << 20

func (s *browserSession) beginResponse(ctx context.Context) (func(), bool) {
	slot, ok := ctx.Value(requestSlotKey{}).(*requestSlot)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ok || ctx.Err() != nil || s.cookie == "" || !time.Now().Before(s.deadline) || (s.bound && (slot.channel == nil || s.channel != slot.channel)) {
		return nil, false
	}
	if _, exists := s.active[slot]; !exists {
		return nil, false
	}
	write := &responseWrite{done: make(chan struct{}), channel: slot.channel}
	s.writes[write] = struct{}{}
	return func() {
		s.mu.Lock()
		delete(s.writes, write)
		close(write.done)
		s.mu.Unlock()
	}, true
}

// writeJSON emits a bounded, complete response only while its admitted session is valid.
// It is intended for the standard net/http server writer, not streaming or WebSocket.
func (s *browserSession) writeJSON(w http.ResponseWriter, r *http.Request, status int, body []byte) {
	if len(body) > maxResponseBytes {
		status, body = http.StatusInternalServerError, []byte(`{"error":"response too large"}`)
	}
	if _, ok := w.(interface{ FlushError() error }); !ok {
		panic(http.ErrAbortHandler)
	}
	controller := http.NewResponseController(w)
	deadline := time.Now().Add(2 * time.Second)
	if requestDeadline, ok := r.Context().Deadline(); ok && requestDeadline.Before(deadline) {
		deadline = requestDeadline
	}
	if err := controller.SetWriteDeadline(deadline); err != nil {
		panic(http.ErrAbortHandler)
	}
	if err := controller.SetReadDeadline(deadline); err != nil {
		panic(http.ErrAbortHandler)
	}
	finish, ok := s.beginResponse(r.Context())
	if !ok {
		panic(http.ErrAbortHandler)
	}
	defer finish()
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(r.Context(), func() {
		_ = controller.SetReadDeadline(time.Now())
		_ = controller.SetWriteDeadline(time.Now())
		close(callbackDone)
	})
	defer func() {
		if !stop() {
			<-callbackDone
		}
	}()
	if r.Context().Err() != nil {
		panic(http.ErrAbortHandler)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if len(body) > 0 {
		if n, err := w.Write(body); err != nil || n != len(body) {
			panic(http.ErrAbortHandler)
		}
	}
	if err := controller.Flush(); err != nil {
		panic(http.ErrAbortHandler)
	}
}
