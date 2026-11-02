package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/vitalcc55/gk80-control/internal/contract"
)

// pairHTTP is the only unauthenticated entry that can create a browser session.
func (s *browserSession) pairHTTP(w http.ResponseWriter, r *http.Request) {
	deny := func(status int) { closeUnreadRequest(w); http.Error(w, "session unavailable", status) }
	w.Header().Set("Cache-Control", "no-store")
	origins := r.Header.Values("Origin")
	if r.Method != http.MethodPost || r.Host != s.host || r.URL.RawQuery != "" || r.URL.ForceQuery || len(origins) != 1 || origins[0] != "http://"+s.host {
		deny(http.StatusForbidden)
		return
	}
	select {
	case s.pairing <- struct{}{}:
		defer func() { <-s.pairing }()
	default:
		deny(http.StatusTooManyRequests)
		return
	}
	body, err := readJSON(w, r)
	if err != nil {
		deny(http.StatusBadRequest)
		return
	}
	if _, err := contract.CanonicalJSON(body); err != nil {
		deny(http.StatusBadRequest)
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || len(fields) != 1 {
		deny(http.StatusBadRequest)
		return
	}
	var code string
	if json.Unmarshal(fields["code"], &code) != nil || code == "" {
		deny(http.StatusBadRequest)
		return
	}
	cookie, proof, err := s.pair(code)
	if err != nil {
		deny(http.StatusUnauthorized)
		return
	}
	response, err := json.Marshal(struct {
		Proof string `json:"proof"`
	}{Proof: proof})
	if err != nil {
		deny(http.StatusServiceUnavailable)
		return
	}
	s.mu.Lock()
	if r.Context().Err() != nil || !time.Now().Before(s.deadline) || !sameToken(proof, s.proof) || !sameToken(cookie.Value, s.cookie) {
		s.mu.Unlock()
		s.revoke()
		deny(http.StatusUnauthorized)
		return
	}
	deadline := time.Now().Add(2 * time.Second)
	if s.deadline.Before(deadline) {
		deadline = s.deadline
	}
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	slot := &requestSlot{cancel: cancel}
	write := &responseWrite{done: make(chan struct{})}
	s.active[slot] = struct{}{}
	s.writes[write] = struct{}{}
	s.mu.Unlock()
	controller := http.NewResponseController(w)
	writeErr := controller.SetWriteDeadline(deadline)
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = controller.SetWriteDeadline(time.Now()); close(callbackDone) })
	responded := false
	if writeErr == nil && ctx.Err() == nil {
		http.SetCookie(w, cookie)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		responded = true
		var n int
		n, writeErr = w.Write(response)
		if writeErr == nil && n != len(response) {
			writeErr = errors.New("incomplete pairing response")
		}
		if writeErr == nil {
			writeErr = controller.Flush()
		}
	}
	if ctx.Err() != nil && writeErr == nil {
		writeErr = ctx.Err()
	}
	if !stop() {
		<-callbackDone
	}
	cancel()
	s.mu.Lock()
	delete(s.active, slot)
	delete(s.writes, write)
	close(write.done)
	if writeErr != nil && sameToken(proof, s.proof) {
		s.cookie, s.proof = "", ""
	}
	s.mu.Unlock()
	if writeErr != nil {
		s.revoke()
		if !responded {
			deny(http.StatusServiceUnavailable)
		}
	}
}
