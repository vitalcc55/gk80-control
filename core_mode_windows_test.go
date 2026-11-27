//go:build desktopapp

package main

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

func TestDesktopExecutableRunsCore(t *testing.T) {
	executable, err := filepath.Abs(filepath.Join("bin", "gk80-desktop.exe"))
	if err != nil {
		t.Fatal(err)
	}
	profileDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	process, handshake, err := transport.Start(ctx, executable, profileDir, profileID, appVersion, coreArgument)
	if process != nil {
		t.Cleanup(func() { _ = process.Close(context.Background()) })
	}
	if err != nil {
		t.Fatalf("start the built desktop executable as Core: %v", err)
	}
	if !handshake.Demo || handshake.DatabaseId == uuid.Nil {
		t.Fatalf("unexpected Core handshake: %+v", handshake)
	}
	if err := process.Close(ctx); err != nil {
		t.Fatalf("close the owned Core: %v", err)
	}
	if code, exited := process.ExitCode(); !exited || code != 0 {
		t.Fatalf("Core exit: code %d, confirmed %t", code, exited)
	}

	bad := exec.CommandContext(ctx, executable, coreArgument)
	bad.Stdin = strings.NewReader("{}\n")
	output, err := bad.CombinedOutput()
	var failed *exec.ExitError
	if !errors.As(err, &failed) || failed.ExitCode() != 1 || !strings.Contains(string(output), "GK80 core could not start") {
		t.Fatalf("invalid bootstrap: %v, output %q", err, output)
	}
}
