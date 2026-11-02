package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
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

func (s *browserSession) openChannel(w http.ResponseWriter, r *http.Request) {
	deny := func(status int) { closeUnreadRequest(w); http.Error(w, "session unavailable", status) }
	origins := r.Header.Values("Origin")
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.URL.RawQuery != "" || r.URL.ForceQuery || r.Host != s.host || len(origins) != 1 || origins[0] != "http://"+s.host {
		deny(http.StatusForbidden)
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		deny(http.StatusServiceUnavailable)
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
	if s.channel != nil {
		closing := s.channel.ctx.Err() != nil
		generation := s.channel.generation
		s.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		message, _ := json.Marshal(struct {
			Busy    bool   `json:"busy"`
			Channel string `json:"channel"`
		}{Busy: true, Channel: generation})
		if closing {
			message = []byte(`{"retry":true}`)
		}
		_ = conn.Write(ctx, websocket.MessageText, message)
		cancel()
		_ = conn.Close(websocket.StatusPolicyViolation, "profile in use")
		return
	}
	s.sequence++
	generation := s.instance + ":" + strconv.FormatUint(s.sequence, 10)
	ctx, cancel := context.WithCancel(context.Background())
	channel := &browserChannel{generation: generation, ctx: ctx, cancel: cancel, close: func() {
		_ = conn.CloseNow()
		_ = upgrader.conn.Close()
	}}
	s.channel = channel
	s.mu.Unlock()
	defer s.closeChannel(channel)
	message, err := json.Marshal(struct {
		Channel string `json:"channel"`
	}{Channel: generation})
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
	channel.close()
	for _, done := range waits {
		<-done
	}
	s.mu.Lock()
	if s.channel == channel {
		s.channel = nil
	}
	s.mu.Unlock()
}
