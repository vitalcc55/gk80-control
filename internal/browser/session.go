// Package browser contains the local browser host boundary for the demo.
package browser

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const sessionCookie = "gk80_browser_session"

var errSessionDenied = errors.New("browser session denied")

type browserSession struct {
	mu        sync.Mutex
	host      string
	code      string
	codeUntil time.Time
	validFor  time.Duration
	attempts  int
	cookie    string
	proof     string
	deadline  time.Time
	timer     *time.Timer
	done      chan struct{}
	active    map[*requestSlot]struct{}
}

type requestSlot struct{ cancel context.CancelFunc }

func randomToken() (string, error) {
	var bytes [24]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes[:]), nil
}

func newSession(host string, validFor time.Duration) (*browserSession, string, error) {
	address, port, err := net.SplitHostPort(host)
	portNumber, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || address != "127.0.0.1" || portErr != nil || portNumber == 0 || validFor <= 0 {
		return nil, "", errSessionDenied
	}
	code, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	return &browserSession{host: host, code: code, codeUntil: time.Now().Add(validFor), validFor: validFor}, code, nil
}

// admit registers cancellation under the same lock used to revoke a session.
// Callers must still respect the returned context before acting on a request.
func (s *browserSession) admit(r *http.Request) (context.Context, func(), int) {
	cookie, err := r.Cookie(sessionCookie)
	s.mu.Lock()
	valid := err == nil && time.Now().Before(s.deadline) && sameToken(cookie.Value, s.cookie) && sameToken(strings.TrimSpace(r.Header.Get("X-GK80-Session")), s.proof)
	if !valid {
		s.mu.Unlock()
		return nil, nil, http.StatusUnauthorized
	}
	if len(s.active) >= 64 {
		s.mu.Unlock()
		return nil, nil, http.StatusTooManyRequests
	}
	ctx, cancel := context.WithDeadline(r.Context(), s.deadline)
	slot := &requestSlot{cancel: cancel}
	s.active[slot] = struct{}{}
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.active, slot)
		s.mu.Unlock()
	}, 0
}

func sameToken(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *browserSession) pair(code string) (*http.Cookie, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.code == "" || !time.Now().Before(s.codeUntil) || s.attempts >= 8 {
		return nil, "", errSessionDenied
	}
	s.attempts++
	if !sameToken(s.code, code) {
		return nil, "", errSessionDenied
	}
	cookie, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	proof, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	s.code = ""
	s.cookie, s.proof = cookie, proof
	s.deadline = time.Now().Add(s.validFor)
	s.active = make(map[*requestSlot]struct{})
	s.done = make(chan struct{})
	s.timer = time.AfterFunc(s.validFor, s.revoke)
	return &http.Cookie{Name: sessionCookie, Value: cookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode}, proof, nil
}

func (s *browserSession) revoke() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.code, s.cookie, s.proof = "", "", ""
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	for slot := range s.active {
		slot.cancel()
		delete(s.active, slot)
	}
	if s.done != nil {
		close(s.done)
		s.done = nil
	}
}

func (s *browserSession) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+s.host) || (r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Origin") != "http://"+s.host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ctx, cancel, status := s.admit(r)
		if status != 0 {
			http.Error(w, "session unavailable", status)
			return
		}
		defer cancel()
		if ctx.Err() != nil {
			http.Error(w, "request cancelled", http.StatusRequestTimeout)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
