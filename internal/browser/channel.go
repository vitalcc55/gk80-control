package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type channelUpgradeWriter struct {
	http.ResponseWriter
	conn *channelTransport
}

type channelTransport struct {
	net.Conn
	once sync.Once
	err  error
}

func (c *channelTransport) Close() error {
	c.once.Do(func() { c.err = c.Conn.Close() })
	return c.err
}

func (w *channelUpgradeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buffer, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.conn = &channelTransport{Conn: conn}
		conn = w.conn
	}
	return conn, buffer, err
}

// newBoundRoutes requires an authenticated live browser channel for application HTTP.
// The separate unbound router remains useful for exercising the service in isolation.
func newBoundRoutes(s *browserSession, service browserOperations, bootstrap bootstrapOperations) http.Handler {
	s.mu.Lock()
	s.bound = true
	s.mu.Unlock()
	app := newRoutes(s, service, bootstrap)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/session/stream" {
			s.openChannel(w, r)
			return
		}
		app.ServeHTTP(w, r)
	})
}

func (s *browserSession) openChannel(w http.ResponseWriter, r *http.Request) {
	deny := func(status int) { closeUnreadRequest(w); http.Error(w, "session unavailable", status) }
	origins := r.Header.Values("Origin")
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.URL.RawQuery != "" || r.URL.ForceQuery || r.Host != s.host || len(origins) != 1 || origins[0] != "http://"+s.host {
		deny(http.StatusForbidden)
		return
	}
	protocolHeaders := r.Header.Values("Sec-WebSocket-Protocol")
	if len(protocolHeaders) != 1 {
		deny(http.StatusUnauthorized)
		return
	}
	protocols := strings.Split(protocolHeaders[0], ",")
	if len(protocols) != 2 || strings.TrimSpace(protocols[0]) != "gk80-v1" || !strings.HasPrefix(strings.TrimSpace(protocols[1]), "gk80-proof.") {
		deny(http.StatusUnauthorized)
		return
	}
	proof := strings.TrimPrefix(strings.TrimSpace(protocols[1]), "gk80-proof.")
	cookie, err := r.Cookie(sessionCookie)
	cookieCount := 0
	for _, item := range r.Cookies() {
		if item.Name == sessionCookie {
			cookieCount++
		}
	}
	capability, tokenErr := randomToken()
	if tokenErr != nil {
		deny(http.StatusServiceUnavailable)
		return
	}
	s.mu.Lock()
	valid := err == nil && cookieCount == 1 && time.Now().Before(s.deadline) && sameToken(cookie.Value, s.cookie) && sameToken(proof, s.proof)
	if !valid {
		s.mu.Unlock()
		deny(http.StatusUnauthorized)
		return
	}
	if s.channel != nil {
		s.mu.Unlock()
		deny(http.StatusConflict)
		return
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		s.mu.Unlock()
		deny(http.StatusServiceUnavailable)
		return
	}
	upgrader := &channelUpgradeWriter{ResponseWriter: w}
	conn, err := websocket.Accept(upgrader, r, &websocket.AcceptOptions{Subprotocols: []string{"gk80-v1"}})
	if err != nil {
		s.mu.Unlock()
		return
	}
	if err := upgrader.conn.SetWriteDeadline(time.Time{}); err != nil {
		s.mu.Unlock()
		_ = conn.CloseNow()
		return
	}
	ctx, cancel := context.WithDeadline(context.Background(), s.deadline)
	channel := &browserChannel{capability: capability, conn: conn, transport: upgrader.conn, ctx: ctx, cancel: cancel}
	s.channel = channel
	s.mu.Unlock()
	defer s.closeChannel(channel)
	message, err := json.Marshal(struct {
		Channel string `json:"channel"`
	}{Channel: capability})
	if err != nil {
		return
	}
	writeCtx, stop := context.WithTimeout(ctx, 2*time.Second)
	err = conn.Write(writeCtx, websocket.MessageText, message)
	stop()
	if err != nil {
		return
	}
	conn.SetReadLimit(1024)
	_, _, _ = conn.Reader(ctx)
}

func (s *browserSession) closeChannel(channel *browserChannel) {
	s.mu.Lock()
	if s.channel == channel {
		channel.cancel()
		for slot := range s.active {
			if slot.channel == channel {
				slot.cancel()
				delete(s.active, slot)
			}
		}
	}
	channel.cancel()
	waits := make([]<-chan struct{}, 0, len(s.writes))
	for write := range s.writes {
		if write.channel == channel {
			waits = append(waits, write.done)
		}
	}
	s.mu.Unlock()
	_ = channel.conn.CloseNow()
	_ = channel.transport.Close()
	for _, done := range waits {
		<-done
	}
	s.mu.Lock()
	if s.channel == channel {
		s.channel = nil
	}
	s.mu.Unlock()
}
