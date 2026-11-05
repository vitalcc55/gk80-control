package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFrameworkErrorsAreExplicitlyUnclassified(t *testing.T) {
	var output memoryWriter
	logger := newLogger(RoleDesktop, uuid.New(), &output, io.Discard, Config{})
	logger.Framework().Error("secret-marker", "error_code", "fatal_error", "operation", "application_run")
	closeLogger(t, logger)
	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(output.contents())), &record); err != nil {
		t.Fatal(err)
	}
	if record["error_code"] != "unclassified" || record["operation"] != nil || record["level"] != "ERROR" {
		t.Fatalf("unknown framework failure misclassified: %s", output.contents())
	}
}

type unsafeFrameworkValue struct{}

func (unsafeFrameworkValue) LogValue() slog.Value { panic("secret-marker must not be resolved") }

func frameworkJSONL(t *testing.T, content string) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func TestFrameworkFailureContextIsCataloguedAndNeverUsesPayload(t *testing.T) {
	for _, tc := range []struct {
		name      string
		code      FrameworkErrorCode
		operation FrameworkOperation
		wantCode  string
		wantOp    string
	}{
		{"fatal", FrameworkFatalError, FrameworkErrorHandler, "fatal_error", "error_handler"},
		{"run", FrameworkApplicationRunFailed, FrameworkApplicationRun, "application_run_failed", "application_run"},
		{"unknown callback", FrameworkUnclassified, FrameworkErrorHandler, "unclassified", "error_handler"},
		{"unknown code", FrameworkErrorCode("secret-marker"), FrameworkErrorHandler, "unclassified", ""},
		{"unknown operation", FrameworkFatalError, FrameworkOperation("secret-marker"), "unclassified", ""},
		{"wrong pair", FrameworkFatalError, FrameworkApplicationRun, "unclassified", ""},
		{"missing operation", FrameworkApplicationRunFailed, "", "unclassified", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output memoryWriter
			runID, forgedID := uuid.New(), uuid.New()
			logger := newLogger(RoleDesktop, runID, &output, io.Discard, Config{QueueSize: 8})
			bound := logger.FrameworkFailure(tc.code, tc.operation).
				With("error_code", "forged", "operation", "forged", "process_run_id", forgedID).
				WithGroup("secret-marker").With("error", unsafeFrameworkValue{})
			levels := []slog.Level{slog.LevelInfo, slog.LevelWarn, slog.LevelError, slog.LevelError + 4}
			for _, level := range levels {
				bound.LogAttrs(context.Background(), level, "secret-marker\nforged JSONL",
					slog.Any("error", errors.Join(errors.New("secret-marker"), errors.New("nested-secret-marker"))),
					slog.Any("payload", map[string]any{"secret": "secret-marker", "lazy": unsafeFrameworkValue{}}),
					slog.Any("lazy", unsafeFrameworkValue{}),
					slog.String("error_code", "application_run_failed"), slog.String("operation", "application_run"),
					slog.String("process_run_id", forgedID.String()), slog.String("backend_run_id", forgedID.String()),
					slog.String("test_id", forgedID.String()), slog.String("command_id", forgedID.String()),
					slog.Group("secret-marker", "cookie", "secret-marker"))
			}
			bound.Debug("secret-marker")
			closeLogger(t, logger)
			if strings.Contains(output.contents(), "secret-marker") || logger.Status().Degraded {
				t.Fatalf("unsafe framework data or unexpected drop: %s %+v", output.contents(), logger.Status())
			}
			records := frameworkJSONL(t, output.contents())
			if len(records) != len(levels) {
				t.Fatalf("unexpected records: %d", len(records))
			}
			for i, record := range records {
				if record["event"] != "framework.message" || record["msg"] != "Framework diagnostic" || record["component"] != "wails" || record["level"] != levels[i].String() || record["error_code"] != tc.wantCode {
					t.Fatalf("wrong framework facts: %+v", record)
				}
				if (tc.wantOp == "" && record["operation"] != nil) || (tc.wantOp != "" && record["operation"] != tc.wantOp) {
					t.Fatalf("untrusted operation: %+v", record)
				}
				if record["process_run_id"] != runID.String() || record["process_role"] != "desktop" || record["pid"] != float64(os.Getpid()) {
					t.Fatalf("process identity changed: %+v", record)
				}
				for _, field := range []string{"backend_run_id", "test_id", "command_id", "payload", "error", "lazy", "secret-marker"} {
					if _, ok := record[field]; ok {
						t.Fatalf("untrusted field %s: %+v", field, record)
					}
				}
			}
		})
	}
}

func TestFrameworkContextReuseDoesNotMultiplyRecords(t *testing.T) {
	var output memoryWriter
	logger := newLogger(RoleDesktop, uuid.New(), &output, io.Discard, Config{QueueSize: 32})
	base := logger.Framework()
	var calls sync.WaitGroup
	for i := range 16 {
		calls.Go(func() {
			code, operation := FrameworkFatalError, FrameworkErrorHandler
			if i%2 == 0 {
				code, operation = FrameworkApplicationRunFailed, FrameworkApplicationRun
			}
			logger.FrameworkFailure(code, operation).With("error_code", "forged").Error("secret-marker")
		})
	}
	calls.Wait()
	base.Error("secret-marker")
	closeLogger(t, logger)
	counts := map[string]int{}
	for _, record := range frameworkJSONL(t, output.contents()) {
		code, _ := record["error_code"].(string)
		counts[code]++
		if code == "unclassified" && record["operation"] != nil {
			t.Fatal("bound context mutated the base handler")
		}
	}
	if len(counts) != 3 || counts["fatal_error"] != 8 || counts["application_run_failed"] != 8 || counts["unclassified"] != 1 || logger.Status().Degraded {
		t.Fatalf("context changed or duplicate delivery: %+v %+v", counts, logger.Status())
	}
}

func TestOrdinaryFrameworkLevelsAndLibraryDoNotInventFailures(t *testing.T) {
	var output memoryWriter
	logger := newLogger(RoleDesktop, uuid.New(), &output, io.Discard, Config{})
	logger.Framework().Info("secret-marker")
	logger.Framework().Warn("secret-marker")
	logger.Library().Error("secret-marker")
	logger.Framework().Log(context.Background(), slog.LevelError+4, "secret-marker")
	closeLogger(t, logger)
	records := frameworkJSONL(t, output.contents())
	if len(records) != 4 {
		t.Fatalf("unexpected records: %d", len(records))
	}
	for i, record := range records {
		if (i < 3 && record["error_code"] != nil) || (i == 3 && record["error_code"] != "unclassified") || record["operation"] != nil {
			t.Fatalf("invented failure facts: %+v", record)
		}
	}
}

func TestFrameworkFailureWithBrokenWriterKeepsResultAndOneFallback(t *testing.T) {
	notice := noticeWriter{delivered: make(chan string, 2)}
	logger := newLogger(RoleDesktop, uuid.New(), shortWriter{}, notice, Config{})
	want := errors.New("subject result")
	operation := func() error {
		logger.FrameworkFailure(FrameworkFatalError, FrameworkErrorHandler).Error("secret-marker")
		return want
	}
	if operation() != want {
		t.Fatal("diagnostics changed subject result")
	}
	closeLogger(t, logger)
	if status := logger.Status(); !status.Degraded || status.WriterErrors != 1 || status.DroppedError != 1 {
		t.Fatalf("unexpected writer failure accounting: %+v", status)
	}
	if len(notice.delivered) != 1 || <-notice.delivered != "GK80 diagnostic writer unavailable or records dropped\n" {
		t.Fatal("fallback leaked data, recursed or multiplied")
	}
}

func TestFrameworkFailureCannotWaitForBlockedWriterOrOverflow(t *testing.T) {
	writer := &blockedWriter{started: make(chan struct{}), release: make(chan struct{})}
	logger := newLogger(RoleDesktop, uuid.New(), writer, io.Discard, Config{QueueSize: 1})
	defer func() { close(writer.release); closeLogger(t, logger) }()
	bound := logger.FrameworkFailure(FrameworkFatalError, FrameworkErrorHandler)
	bound.Error("secret-marker")
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	bound.Error("secret-marker")
	want := errors.New("subject result")
	result := make(chan error, 1)
	go func() { bound.Error("secret-marker"); result <- want }()
	select {
	case got := <-result:
		if got != want {
			t.Fatal("diagnostics changed subject result")
		}
	case <-time.After(time.Second):
		t.Fatal("diagnostics blocked subject result")
	}
	if status := logger.Status(); !status.Degraded || status.DroppedError != 1 {
		t.Fatalf("overflow was not bounded: %+v", status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if !errors.Is(logger.Close(ctx), context.DeadlineExceeded) {
		t.Fatal("diagnostics close was not bounded")
	}
}
