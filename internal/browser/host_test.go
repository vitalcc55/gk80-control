package browser

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
)

func TestHostReportsMissingEntryAsset(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	fake := &routeService{}
	_, _, err = newHost(listener, fake, fake, fstest.MapFS{}, time.Minute, time.Hour)
	if !errors.Is(err, ErrAssetsUnavailable) {
		t.Fatalf("missing entry: %v", err)
	}
}

func TestHostShutdownBoundsStalledChannelClose(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	blocked := &delayedCloseListener{Listener: listener, entered: make(chan struct{}), release: make(chan struct{})}
	fake := &routeService{}
	host, code, err := newHost(blocked, fake, fake, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("demo")}}, time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cookie, proof, err := host.session.pair(code)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = host.Serve() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	base := "http://" + listener.Addr().String()
	headers := http.Header{"Origin": []string{base}, "Cookie": []string{cookie.String()}}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/api/session/stream", &websocket.DialOptions{HTTPHeader: headers, Subprotocols: []string{"gk80-v1", "gk80-proof." + proof}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	_, body, err := conn.Read(ctx)
	if err != nil || !json.Valid(body) {
		t.Fatal("channel not admitted")
	}
	blocked.armed.Store(true)
	deadline, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	result := make(chan error, 1)
	go func() { result <- host.Shutdown(deadline) }()
	select {
	case <-blocked.entered:
	case <-ctx.Done():
		t.Fatal("channel close did not start")
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("stalled shutdown was reported as confirmed")
		}
	case <-ctx.Done():
		t.Fatal("shutdown exceeded its deadline")
	}
	close(blocked.release)
	if err := host.Shutdown(ctx); err != nil {
		t.Fatalf("cleanup after release: %v", err)
	}
}

type blockedAssets struct {
	fs.FS
	entered chan struct{}
	release chan struct{}
}

func (a blockedAssets) Open(name string) (fs.File, error) {
	if name == "slow.js" {
		a.entered <- struct{}{}
		<-a.release
	}
	return a.FS.Open(name)
}

func TestHostBoundsConcurrentAssetReads(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	assets := blockedAssets{FS: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("demo")}, "slow.js": &fstest.MapFile{Data: []byte("script")}}, entered: make(chan struct{}, 4), release: make(chan struct{})}
	fake := &routeService{}
	host, _, err := newHost(listener, fake, fake, assets, time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = host.Serve() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() {
		if err := host.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	defer close(assets.release)
	url := "http://" + listener.Addr().String() + "/slow.js"
	for range 4 {
		go func() {
			r, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if response, err := http.DefaultClient.Do(r); err == nil {
				_ = response.Body.Close()
			}
		}()
	}
	for range 4 {
		select {
		case <-assets.entered:
		case <-ctx.Done():
			t.Fatal("asset requests did not start")
		}
	}
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("asset admission: %d", response.StatusCode)
	}
}

func TestHostServesOnlyAssetsAndBoundApplicationRoutes(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &routeService{}
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("demo entry")}, "assets/app.js": &fstest.MapFile{Data: []byte("demo script")}}
	host, _, err := newHost(listener, fake, fake, assets, time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	serve := make(chan error, 1)
	go func() { serve <- host.Serve() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() {
		if err := host.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	base := "http://" + listener.Addr().String()
	request := func(method, path, hostHeader string) (int, string) {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, method, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if hostHeader != "" {
			r.Host = hostHeader
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(data)
	}
	if code, body := request(http.MethodGet, "/", ""); code != 200 || body != "demo entry" {
		t.Fatalf("entry: %d %q", code, body)
	}
	if code, _ := request(http.MethodGet, "/api/status", ""); code != http.StatusUnauthorized {
		t.Fatalf("unbound API: %d", code)
	}
	if code, _ := request(http.MethodGet, "/", "other.invalid"); code != http.StatusForbidden {
		t.Fatalf("host mismatch: %d", code)
	}
	if code, body := request(http.MethodGet, "/assets/", ""); code != http.StatusNotFound || strings.Contains(body, "app.js") {
		t.Fatalf("directory listing: %d", code)
	}
	if code, _ := request(http.MethodPost, "/", ""); code != http.StatusMethodNotAllowed {
		t.Fatalf("asset POST: %d", code)
	}
	if err := host.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serve:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("host did not stop")
	}
}
