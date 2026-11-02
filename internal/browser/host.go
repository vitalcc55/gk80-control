package browser

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vitalcc55/gk80-control/internal/desktop"
)

var ErrAssetsUnavailable = errors.New("browser assets unavailable")

// Host serves the local development browser entry and demo routes.
type Host struct {
	listener net.Listener
	server   *http.Server
	session  *browserSession
	closing  atomic.Bool
}

// NewHost binds an existing loopback listener to the supplied owner and assets.
func NewHost(listener net.Listener, owner *desktop.Owner, assets fs.FS) (*Host, error) {
	if owner == nil {
		return nil, errSessionDenied
	}
	return newHost(listener, desktop.NewService(owner), desktop.NewBootstrapService(owner), assets)
}

func newHost(listener net.Listener, service browserOperations, bootstrap bootstrapOperations, assets fs.FS) (*Host, error) {
	if listener == nil || assets == nil {
		return nil, errSessionDenied
	}
	entry, err := fs.Stat(assets, "index.html")
	if err != nil || entry.IsDir() {
		return nil, ErrAssetsUnavailable
	}
	s, err := newSession(listener.Addr().String())
	if err != nil {
		return nil, err
	}
	api := newRoutes(s, service, bootstrap)
	assetsGate := make(chan struct{}, 4)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.host {
			closeUnreadRequest(w)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			api.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			closeUnreadRequest(w)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			closeUnreadRequest(w)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		select {
		case assetsGate <- struct{}{}:
			defer func() { <-assetsGate }()
		default:
			http.Error(w, "assets busy", http.StatusTooManyRequests)
			return
		}
		if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
			http.Error(w, "asset unavailable", http.StatusServiceUnavailable)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if !fs.ValidPath(name) {
			http.NotFound(w, r)
			return
		}
		file, err := assets.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer func() { _ = file.Close() }()
		info, err := file.Stat()
		if err != nil || info.IsDir() || info.Size() > 16<<20 {
			http.NotFound(w, r)
			return
		}
		data, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
		if err != nil || len(data) > 16<<20 {
			http.Error(w, "asset unavailable", http.StatusInternalServerError)
			return
		}
		if kind := mime.TypeByExtension(path.Ext(name)); kind != "" {
			w.Header().Set("Content-Type", kind)
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, path.Base(name), info.ModTime(), bytes.NewReader(data))
	})
	host := &Host{listener: listener, session: s}
	host.server = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8 << 10}
	return host, nil
}

func (h *Host) Serve() error {
	err := h.server.Serve(h.listener)
	if errors.Is(err, http.ErrServerClosed) || (h.closing.Load() && errors.Is(err, net.ErrClosed)) {
		return nil
	}
	return err
}

// Shutdown revokes the browser session before waiting for ordinary HTTP requests.
func (h *Host) Shutdown(ctx context.Context) error {
	h.closing.Store(true)
	_ = h.listener.Close()
	done := make(chan struct{})
	go func() { h.session.revoke(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		_ = h.server.Close()
		return errors.New("browser session shutdown unconfirmed")
	}
	if err := h.server.Shutdown(ctx); err != nil {
		_ = h.server.Close()
		return err
	}
	return nil
}
