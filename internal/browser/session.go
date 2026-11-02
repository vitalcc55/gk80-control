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

	"github.com/coder/websocket"
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
	writes    map[*responseWrite]struct{}
	bound     bool
	channel   *browserChannel
	revoking  chan struct{}
	pairing   chan struct{}
}

type requestClass uint8

const (
	ordinaryRequest requestClass = iota
	stopRequest
	heartbeatRequest
)

type requestSlot struct {
	cancel  context.CancelFunc
	class   requestClass
	channel *browserChannel
}

type requestSlotKey struct{}
type responseWrite struct {
	done    chan struct{}
	channel *browserChannel
}

type browserChannel struct {
	capability string
	conn       *websocket.Conn
	transport  *channelTransport
	ctx        context.Context
	cancel     context.CancelFunc
}

// actionAllowed is the admission point for an already authenticated operation.
// An operation admitted before revocation can still have an unknown outcome.
func (s *browserSession) actionAllowed(ctx context.Context) bool {
	slot, ok := ctx.Value(requestSlotKey{}).(*requestSlot)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ok || ctx.Err() != nil || s.cookie == "" || !time.Now().Before(s.deadline) || (s.bound && (slot.channel == nil || s.channel != slot.channel)) {
		return false
	}
	_, ok = s.active[slot]
	return ok
}

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
	return &browserSession{host: host, code: code, codeUntil: time.Now().Add(validFor), validFor: validFor, pairing: make(chan struct{}, 2)}, code, nil
}

// admitClass registers cancellation under the same lock used to revoke a session.
// The host selects the class by its fixed route; handlers must respect cancellation.
func (s *browserSession) admitClass(r *http.Request, class requestClass) (context.Context, func(), int) {
	cookie, err := r.Cookie(sessionCookie)
	s.mu.Lock()
	valid := err == nil && time.Now().Before(s.deadline) && sameToken(cookie.Value, s.cookie) && sameToken(strings.TrimSpace(r.Header.Get("X-GK80-Session")), s.proof)
	if s.bound {
		values := r.Header.Values("X-GK80-Channel")
		valid = valid && len(values) == 1 && s.channel != nil && s.channel.ctx.Err() == nil && sameToken(strings.TrimSpace(values[0]), s.channel.capability)
	}
	if !valid {
		s.mu.Unlock()
		return nil, nil, http.StatusUnauthorized
	}
	limit := 2
	if class == ordinaryRequest {
		limit = 60
	} else if class != stopRequest && class != heartbeatRequest {
		s.mu.Unlock()
		return nil, nil, http.StatusForbidden
	}
	count := 0
	for slot := range s.active {
		if slot.class == class {
			count++
		}
	}
	if count >= limit {
		s.mu.Unlock()
		return nil, nil, http.StatusTooManyRequests
	}
	ctx, cancel := context.WithDeadline(r.Context(), s.deadline)
	slot := &requestSlot{cancel: cancel, class: class, channel: s.channel}
	s.active[slot] = struct{}{}
	s.mu.Unlock()
	return context.WithValue(ctx, requestSlotKey{}, slot), func() {
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
	s.writes = make(map[*responseWrite]struct{})
	s.done = make(chan struct{})
	s.timer = time.AfterFunc(s.validFor, s.revoke)
	return &http.Cookie{Name: sessionCookie, Value: cookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode}, proof, nil
}

func (s *browserSession) revoke() {
	s.mu.Lock()
	if s.revoking != nil {
		done := s.revoking
		s.mu.Unlock()
		<-done
		return
	}
	done := make(chan struct{})
	s.revoking = done
	s.code, s.cookie, s.proof = "", "", ""
	channel := s.channel
	s.channel = nil
	if channel != nil {
		channel.cancel()
	}
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
	waits := make([]<-chan struct{}, 0, len(s.writes))
	for write := range s.writes {
		waits = append(waits, write.done)
	}
	s.mu.Unlock()
	if channel != nil {
		_ = channel.conn.CloseNow()
		_ = channel.transport.Close()
	}
	for _, done := range waits {
		<-done
	}
	s.mu.Lock()
	close(done)
	s.revoking = nil
	s.mu.Unlock()
}

func (s *browserSession) protect(next http.Handler) http.Handler {
	return s.protectClass(ordinaryRequest, next)
}

func closeUnreadRequest(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
	_ = http.NewResponseController(w).SetReadDeadline(time.Now())
}

func (s *browserSession) protectClass(class requestClass, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+s.host) || (r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Origin") != "http://"+s.host) {
			closeUnreadRequest(w)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ctx, cancel, status := s.admitClass(r, class)
		if status != 0 {
			closeUnreadRequest(w)
			http.Error(w, "session unavailable", status)
			return
		}
		defer cancel()
		if ctx.Err() != nil {
			closeUnreadRequest(w)
			http.Error(w, "request cancelled", http.StatusRequestTimeout)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
