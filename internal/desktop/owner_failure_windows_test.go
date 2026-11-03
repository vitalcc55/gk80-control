//go:build windows

package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/diagnostics"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

func TestOwnerLogsUnexpectedCoreExit(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "gk80-core.exe")
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", executable, "./cmd/gk80-core")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build core: %v %s", err, output)
	}

	profile := t.TempDir()
	hostID := uuid.New()
	logger, err := diagnostics.Open(filepath.Join(profile, "logs"), diagnostics.RoleDesktop, hostID, io.Discard, diagnostics.Config{QueueSize: 64, MaxBytes: 1 << 20, AppVersion: "0.1.0", ProfileID: "test"})
	if logger != nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := logger.Close(ctx); err != nil {
				t.Errorf("close diagnostic: %v", err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	if logger == nil {
		t.Fatal("diagnostic logger unavailable")
	}
	changes := make(chan struct{}, 1)
	owner := NewOwner(hostID, executable, profile, "test", "0.1.0", logger.Application(diagnostics.ComponentTransport), logger.Degraded(), false, func(contract.DesktopStatus) {
		select {
		case changes <- struct{}{}:
		default:
		}
	})
	killed := false
	t.Cleanup(func() {
		err := owner.Close()
		if !killed && err != nil {
			t.Errorf("close owner: %v", err)
		}
		if killed && (err == nil || errors.Is(err, transport.ErrCleanupUnconfirmed) || errors.Is(err, errOwnerShutdownUnconfirmed) || errors.Is(err, ErrSessionShutdownUnconfirmed) || !owner.CoreExitConfirmed()) {
			t.Errorf("killed core cleanup: exitConfirmed=%v err=%v", owner.CoreExitConfirmed(), err)
		}
	})
	owner.Start()
	waitProcessReady(t, owner, changes)
	ready := owner.Status()
	if ready.Handshake == nil {
		t.Fatal("ready status has no handshake")
	}
	backendID, databaseID := ready.Handshake.BackendRunId, ready.Handshake.DatabaseId
	logPath := filepath.Join(profile, "logs", "desktop-"+hostID.String()+".jsonl")
	type diagnosticRecord struct {
		Event        string `json:"event"`
		ProcessRole  string `json:"process_role"`
		ProcessRunID string `json:"process_run_id"`
		BackendRunID string `json:"backend_run_id"`
		PID          int    `json:"pid"`
		ExitCode     *int   `json:"exit_code"`
	}
	readEvents := func(path string) []diagnosticRecord {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var events []diagnosticRecord
		for _, line := range strings.Split(string(data), "\n") {
			if line == "" {
				continue
			}
			var event diagnosticRecord
			if json.Unmarshal([]byte(line), &event) == nil {
				events = append(events, event)
			}
		}
		return events
	}
	waitFor := func(predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !predicate() {
			if time.Now().After(deadline) {
				t.Fatal("diagnostic record was not written")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	hasEvent := func(name string, code *int) bool {
		for _, event := range readEvents(logPath) {
			if event.Event != name || event.ProcessRole != "desktop" || event.ProcessRunID != hostID.String() || event.PID != os.Getpid() {
				continue
			}
			if name != string(diagnostics.ConnectionFailed) && event.BackendRunID != backendID.String() {
				continue
			}
			if code == nil || (event.ExitCode != nil && *event.ExitCode == *code) {
				return true
			}
		}
		return false
	}
	waitFor(func() bool { return hasEvent(string(diagnostics.ConnectionConfirmed), nil) })

	owner.mu.Lock()
	process, ok := owner.process.(*transport.Process)
	owner.mu.Unlock()
	if !ok || process == nil {
		t.Fatal("owner did not retain the native child process")
	}
	coreLog := filepath.Join(profile, "logs", "core-"+backendID.String()+".jsonl")
	waitFor(func() bool {
		for _, event := range readEvents(coreLog) {
			if event.ProcessRunID == backendID.String() && event.PID == process.PID() {
				return true
			}
		}
		return false
	})
	child, err := os.FindProcess(process.PID())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Release() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	identity, err := process.Handshake(ctx)
	cancel()
	if err != nil || identity.BackendRunId != backendID || identity.DatabaseId != databaseID {
		t.Fatalf("owned core identity changed: %v", err)
	}
	select {
	case <-process.Done():
		t.Fatal("core exited before the fault")
	default:
	}
	if err := child.Kill(); err != nil {
		t.Fatalf("terminate owned core: %v", err)
	}
	killed = true
	select {
	case <-owner.workerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("owner did not observe the core exit")
	}
	code, confirmed := process.ExitCode()
	if !confirmed || code == 0 || !owner.CoreExitConfirmed() {
		t.Fatalf("core exit not confirmed: code=%d confirmed=%v", code, confirmed)
	}
	waitFor(func() bool {
		return hasEvent(string(diagnostics.ConnectionFailed), &code) && hasEvent(string(diagnostics.CoreStopped), &code)
	})
	if status := logger.Status(); status.Degraded || status.DroppedInfo != 0 || status.DroppedWarn != 0 || status.DroppedError != 0 {
		t.Fatalf("host diagnostic degraded: %+v", status)
	}
}
