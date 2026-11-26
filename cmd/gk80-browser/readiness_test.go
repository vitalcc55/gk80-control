package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/desktop"
)

func TestReportKeepsIndependentShutdownWarnings(t *testing.T) {
	var output bytes.Buffer
	report(&output, errors.Join(errDiagnostics, errHostShutdown, errors.New("secret-marker")))
	for _, expected := range []string{"Журнал диагностики", "Завершение браузерного host"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %q", expected)
		}
	}
	if strings.Contains(output.String(), "secret-marker") {
		t.Fatal("raw error leaked")
	}
}

func TestFailedStartupNeedsConfirmedCleanupBeforeRemovingArtifacts(t *testing.T) {
	for _, cause := range []error{errPort, errProfile, errProfilePath, errCore, errAssets} {
		if code := failureExitCode(cause); code != 2 {
			t.Errorf("%v: confirmed cleanup gave code %d", cause, code)
		}
		for _, unconfirmed := range []error{errOwnerClose, errCoreExit, errHostShutdown} {
			if code := failureExitCode(errors.Join(cause, unconfirmed)); code != 1 {
				t.Errorf("%v with %v: unconfirmed cleanup gave code %d", cause, unconfirmed, code)
			}
		}
	}
}

func TestConsoleRequiresExplicitExit(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	readConsoleExit(strings.NewReader("help\r\n"), stop)
	if ctx.Err() != nil {
		t.Fatal("EOF stopped the browser session")
	}
	readConsoleExit(strings.NewReader(" exit \r\n"), stop)
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("exit did not stop the browser session")
	}
}

func TestWaitReadyDistinguishesCancelAndTimeout(t *testing.T) {
	owner := desktop.NewOwner(uuid.New(), "", "", "", "", slog.New(slog.NewTextHandler(io.Discard, nil)), nil, false, func(contract.DesktopStatus) {})
	defer func() { _ = owner.Close() }()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitReady(cancelled, owner); !errors.Is(err, context.Canceled) || errors.Is(err, errCore) {
		t.Fatalf("cancel: %v", err)
	}
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if err := waitReady(expired, owner); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errCore) {
		t.Fatalf("timeout: %v", err)
	}
}
