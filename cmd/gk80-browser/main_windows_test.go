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
	report(&output, errors.Join(errPairWindow, errDiagnostics, errHostShutdown, errors.New("secret-marker")))
	for _, expected := range []string{"окно сопряжения", "Журнал диагностики", "Завершение браузерного host"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %q", expected)
		}
	}
	if strings.Contains(output.String(), "secret-marker") {
		t.Fatal("raw error leaked")
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
