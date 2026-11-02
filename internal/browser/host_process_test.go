package browser

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/desktop"
)

func TestHostConnectsAutomaticallyToSeparateCoreAndClosesIt(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "gk80-core")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", executable, "./cmd/gk80-core")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build core: %v %s", err, output)
	}
	owner := desktop.NewOwner(uuid.New(), executable, t.TempDir(), "test", "0.1.0", slog.New(slog.NewTextHandler(io.Discard, nil)), nil, false, func(contract.DesktopStatus) {})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("demo")}}
	host, err := NewHost(listener, owner, assets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := host.Shutdown(ctx); err != nil {
			t.Errorf("host cleanup: %v", err)
		}
		if err := owner.Close(); err != nil {
			t.Errorf("core cleanup: %v", err)
		}
	})
	owner.Start()
	served := make(chan error, 1)
	go func() { served <- host.Serve() }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		status := owner.Status()
		if status.Status == contract.DesktopStatusStatusReady && status.Handshake != nil {
			break
		}
		if status.Status != contract.DesktopStatusStatusStarting {
			t.Fatalf("core not ready: %s", status.Status)
		}
		select {
		case <-ctx.Done():
			t.Fatal("core startup timed out")
		case <-time.After(25 * time.Millisecond):
		}
	}
	base := "http://" + listener.Addr().String()
	headers := http.Header{"Origin": []string{base}}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/api/session/stream", &websocket.DialOptions{HTTPHeader: headers, Subprotocols: []string{"gk80-v1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	_, message, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var welcome struct {
		Channel string `json:"channel"`
	}
	if json.Unmarshal(message, &welcome) != nil || welcome.Channel == "" {
		t.Fatal("channel not admitted")
	}
	statusReq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	statusReq.Header.Set("X-GK80-Channel", welcome.Channel)
	response, err := http.DefaultClient.Do(statusReq)
	if err != nil {
		t.Fatal(err)
	}
	var status contract.DesktopStatus
	err = json.NewDecoder(response.Body).Decode(&status)
	_ = response.Body.Close()
	owned := owner.Status()
	if err != nil || response.StatusCode != http.StatusOK || status.Handshake == nil || owned.Handshake == nil || !status.Handshake.Demo || status.Handshake.DatabaseId != owned.Handshake.DatabaseId {
		t.Fatal("host did not expose confirmed core")
	}
	if err := host.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if !owner.CoreExitConfirmed() {
		t.Fatal("owned core exit not confirmed")
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("host did not exit")
	}
}
