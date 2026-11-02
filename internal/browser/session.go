// Package browser contains the local browser host boundary for the demo.
package browser

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var errSessionDenied = errors.New("browser session denied")

type browserSession struct {
	mu       sync.Mutex
	host     string
	instance string
	sequence uint64
	closed   bool
	active   map[*requestSlot]struct{}
	writes   map[*responseWrite]struct{}
	channel  *browserChannel
	revoking chan struct{}
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
	generation string
	close      func()
	ctx        context.Context
	cancel     context.CancelFunc
}

// actionAllowed associates an operation with the current browser connection.
func (s *browserSession) actionAllowed(ctx context.Context) bool {
	slot, ok := ctx.Value(requestSlotKey{}).(*requestSlot)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ok || ctx.Err() != nil || s.closed || slot.channel == nil || s.channel != slot.channel {
		return false
	}
	_, ok = s.active[slot]
	return ok
}

func newSession(host string) (*browserSession, error) {
	address, port, err := net.SplitHostPort(host)
	portNumber, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || address != "127.0.0.1" || portErr != nil || portNumber == 0 {
		return nil, errSessionDenied
	}
	instance, err := uuid.NewRandom()
	if err != nil {
		return nil, err
	}
	return &browserSession{host: host, instance: instance.String(), active: make(map[*requestSlot]struct{}), writes: make(map[*responseWrite]struct{})}, nil
}

// admitClass registers cancellation under the same lock used to revoke a session.
// The host selects the class by its fixed route; handlers must respect cancellation.
func (s *browserSession) admitClass(r *http.Request, class requestClass) (context.Context, func(), int) {
	s.mu.Lock()
	values := r.Header.Values("X-GK80-Channel")
	valid := !s.closed && len(values) == 1 && s.channel != nil && s.channel.ctx.Err() == nil && strings.TrimSpace(values[0]) == s.channel.generation
	if !valid {
		s.mu.Unlock()
		return nil, nil, http.StatusConflict
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
	ctx, cancel := context.WithCancel(r.Context())
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
	s.closed = true
	channel := s.channel
	s.channel = nil
	if channel != nil {
		channel.cancel()
	}
	for slot := range s.active {
		slot.cancel()
		delete(s.active, slot)
	}
	waits := make([]<-chan struct{}, 0, len(s.writes))
	for write := range s.writes {
		waits = append(waits, write.done)
	}
	s.mu.Unlock()
	if channel != nil {
		channel.close()
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
