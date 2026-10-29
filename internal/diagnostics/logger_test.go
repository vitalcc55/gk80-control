package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type memoryWriter struct {
	mu sync.Mutex
	bytes.Buffer
}

func (w *memoryWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Buffer.Write(p)
}

func (w *memoryWriter) Close() error { return nil }

func (w *memoryWriter) contents() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.String()
}

type blockedWriter struct {
	started chan struct{}
	release chan struct{}
}

func (w *blockedWriter) Write(p []byte) (int, error) {
	select {
	case <-w.started:
	default:
		close(w.started)
	}
	<-w.release
	return len(p), nil
}

func (w *blockedWriter) Close() error { return nil }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }
func (shortWriter) Close() error                { return nil }

type closeErrorWriter struct{ memoryWriter }

func (*closeErrorWriter) Close() error { return errors.New("secret-marker") }

type partialWriter struct {
	bytes.Buffer
	writes int
}

func (w *partialWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 2 {
		return w.Buffer.Write(p[:len(p)/2])
	}
	return w.Buffer.Write(p)
}

func (*partialWriter) Close() error { return nil }

type noticeWriter struct{ delivered chan string }

func (w noticeWriter) Write(p []byte) (int, error) {
	w.delivered <- string(p)
	return len(p), nil
}

func closeLogger(t *testing.T, logger *Logger) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := logger.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestEventsHaveSharedIDsAndFrameworkNeverWritesRawData(t *testing.T) {
	var host, core memoryWriter
	commandID, testID, hostID, coreID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	first := newLogger(RoleDesktop, hostID, &host, io.Discard, Config{QueueSize: 8, MaxBytes: 4096})
	second := newLogger(RoleCore, coreID, &core, io.Discard, Config{QueueSize: 8, MaxBytes: 4096})
	first.Application(ComponentTransport).With("command_id", commandID).Info(string(CommandAccepted), "test_id", testID.String(), "payload", "secret-marker")
	second.Application(ComponentCore).Info(string(CommandCompleted), "command_id", commandID.String(), "test_id", testID.String())
	first.Framework().Error("secret-marker", "error", errors.New("secret-marker"), "payload", map[string]string{"key": "secret-marker"})
	closeLogger(t, first)
	closeLogger(t, second)
	if strings.Contains(host.contents(), "secret-marker") {
		t.Fatal("framework payload leaked")
	}
	for _, content := range []string{host.contents(), core.contents()} {
		line := strings.Split(strings.TrimSpace(content), "\n")[0]
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["command_id"] != commandID.String() || record["test_id"] != testID.String() {
			t.Fatalf("correlation IDs missing: %s", line)
		}
		if record["schema_version"] != float64(1) || record["component"] == nil || record["mode"] != "demo" || record["pid"] == nil || record["profile_id"] == nil || record["app_version"] == nil {
			t.Fatalf("common fields missing: %s", line)
		}
	}
}

func TestBlockedWriterDoesNotBlockCallerAndReportsLoss(t *testing.T) {
	writer := &blockedWriter{started: make(chan struct{}), release: make(chan struct{})}
	logger := newLogger(RoleCore, uuid.New(), writer, io.Discard, Config{QueueSize: 1, MaxBytes: 4096})
	logger.Application(ComponentCore).Info(string(ProcessStarted))
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	logger.Application(ComponentCore).Info(string(ProcessStarted))
	logger.Application(ComponentTransport).Error(string(ConnectionFailed))
	status := logger.Status()
	if !status.Degraded || status.DroppedError != 1 {
		t.Fatalf("loss was not reported: %+v", status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if !errors.Is(logger.Close(ctx), context.DeadlineExceeded) {
		t.Fatal("close must respect deadline")
	}
	close(writer.release)
	closeLogger(t, logger)
}

func TestShortWriteIsDetected(t *testing.T) {
	logger := newLogger(RoleDesktop, uuid.New(), shortWriter{}, io.Discard, Config{QueueSize: 2, MaxBytes: 4096})
	logger.Application(ComponentTransport).Error(string(ConnectionFailed))
	closeLogger(t, logger)
	if status := logger.Status(); !status.Degraded || status.WriterErrors == 0 || status.DroppedError != 1 {
		t.Fatalf("short write not detected: %+v", status)
	}
	select {
	case <-logger.Degraded():
	default:
		t.Fatal("degraded notification not delivered")
	}
}

func TestShortWriteLeavesEarlierCompleteRecordReadable(t *testing.T) {
	writer := &partialWriter{}
	logger := newLogger(RoleCore, uuid.New(), writer, io.Discard, Config{QueueSize: 4, MaxBytes: 4096})
	logger.Application(ComponentCore).Info(string(ProcessStarted))
	logger.Application(ComponentCore).Info(string(ProcessStopped))
	closeLogger(t, logger)
	lines := strings.Split(writer.String(), "\n")
	if len(lines) != 2 || !json.Valid([]byte(lines[0])) || json.Valid([]byte(lines[1])) || !logger.Status().Degraded {
		t.Fatalf("unexpected partial log or status: %q %+v", writer.String(), logger.Status())
	}
}

func TestFrameworkDebugIsDisabled(t *testing.T) {
	var output memoryWriter
	logger := newLogger(RoleDesktop, uuid.New(), &output, io.Discard, Config{QueueSize: 2, MaxBytes: 4096})
	if logger.Framework().Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("framework debug must be disabled")
	}
	logger.Framework().Debug("secret-marker")
	closeLogger(t, logger)
	if output.contents() != "" {
		t.Fatalf("unexpected debug output: %s", output.contents())
	}
}

func TestUnknownAndGroupedEventsReportLossWithoutLeakingText(t *testing.T) {
	var output memoryWriter
	logger := newLogger(RoleDesktop, uuid.New(), &output, io.Discard, Config{})
	logger.Application(ComponentDesktop).Error("secret-marker")
	logger.Application(ComponentDesktop).WithGroup("group").Warn(string(ProcessStopped), "command_id", uuid.New())
	logger.Application(ComponentDesktop).Info(string(CommandAccepted), slog.Group("operation", "command_id", uuid.New()))
	logger.Application(ComponentDesktop).With(slog.Group("operation", "command_id", uuid.New())).Info(string(CommandAccepted))
	logger.Application(ComponentDesktop).WithGroup("group").Info(string(ConnectionFailed))
	closeLogger(t, logger)
	status := logger.Status()
	if !status.Degraded || status.DroppedError != 2 || status.DroppedWarn != 1 || status.DroppedInfo != 2 || output.contents() != "" {
		t.Fatalf("event loss was not reported: %+v %s", status, output.contents())
	}
}

func TestApplicationDoesNotLowerSeverityAndLibraryHasOwnSource(t *testing.T) {
	var output memoryWriter
	logger := newLogger(RoleDesktop, uuid.New(), &output, io.Discard, Config{})
	logger.Application(ComponentDesktop).Error(string(ProcessStopped), "exit_code", 1)
	logger.Library().Error("secret-marker")
	closeLogger(t, logger)
	lines := strings.Split(strings.TrimSpace(output.contents()), "\n")
	if len(lines) != 2 || strings.Contains(output.contents(), "secret-marker") {
		t.Fatalf("unexpected records: %s", output.contents())
	}
	var first, second map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if first["level"] != "ERROR" || first["exit_code"] != float64(1) || second["event"] != "library.message" || second["component"] != "library" {
		t.Fatalf("unexpected record metadata: %s", output.contents())
	}
}

func TestEmptyGroupsDoNotDiscardRecords(t *testing.T) {
	var output memoryWriter
	logger := newLogger(RoleCore, uuid.New(), &output, io.Discard, Config{})
	logger.Application(ComponentCore).With(slog.Group("optional")).Info(string(ProcessStarted), slog.Group("empty"))
	closeLogger(t, logger)
	if logger.Status().Degraded || !json.Valid([]byte(strings.TrimSpace(output.contents()))) {
		t.Fatalf("empty group discarded record: %+v %s", logger.Status(), output.contents())
	}
}

func TestOpenFailureReportsDegradedAndUsesSafeFallback(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	notice := noticeWriter{delivered: make(chan string, 1)}
	logger, err := Open(filepath.Join(parent, "secret-marker"), RoleCore, uuid.New(), notice, Config{AppVersion: "0.1.0", ProfileID: "test"})
	if err == nil || logger == nil || !logger.Status().Degraded {
		t.Fatalf("open failure was not reported: %v", err)
	}
	select {
	case text := <-notice.delivered:
		if strings.Contains(text, "secret-marker") {
			t.Fatal("fallback leaked path")
		}
	case <-time.After(time.Second):
		t.Fatal("fallback was not attempted")
	}
	closeLogger(t, logger)
}

func TestFileBudgetIsEnforced(t *testing.T) {
	var output memoryWriter
	logger := newLogger(RoleCore, uuid.New(), &output, io.Discard, Config{QueueSize: 2, MaxBytes: 1})
	logger.Application(ComponentCore).Info(string(ProcessStarted))
	closeLogger(t, logger)
	if output.contents() != "" || !logger.Status().Degraded || logger.Status().DroppedInfo != 1 {
		t.Fatalf("file limit not enforced: %+v", logger.Status())
	}
}

func TestOpenExistingFileReportsOneWriterFailure(t *testing.T) {
	dir, runID := t.TempDir(), uuid.New()
	config := Config{AppVersion: "0.1.0", ProfileID: "test"}
	first, err := Open(dir, RoleCore, runID, io.Discard, config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(dir, RoleCore, runID, io.Discard, config)
	if err == nil {
		t.Fatal("duplicate file must not be reused")
	}
	second.Application(ComponentCore).Info(string(ProcessStarted))
	closeLogger(t, second)
	closeLogger(t, first)
	if status := second.Status(); status.WriterErrors != 1 || status.DroppedInfo != 1 {
		t.Fatalf("unexpected error accounting: %+v", status)
	}
}

func TestCloseWaitsForFallbackOnCloseError(t *testing.T) {
	notice := noticeWriter{delivered: make(chan string, 1)}
	logger := newLogger(RoleCore, uuid.New(), &closeErrorWriter{}, notice, Config{})
	closeLogger(t, logger)
	select {
	case text := <-notice.delivered:
		if strings.Contains(text, "secret-marker") {
			t.Fatal("fallback leaked writer error")
		}
	default:
		t.Fatal("close returned before fallback was attempted")
	}
}

func TestCloseHasLimitWithoutCallerDeadline(t *testing.T) {
	writer := &blockedWriter{started: make(chan struct{}), release: make(chan struct{})}
	logger := newLogger(RoleCore, uuid.New(), writer, io.Discard, Config{})
	logger.Application(ComponentCore).Info(string(ProcessStarted))
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	if !errors.Is(logger.Close(context.Background()), context.DeadlineExceeded) {
		t.Fatal("close without a deadline must be bounded")
	}
	close(writer.release)
	closeLogger(t, logger)
}

func TestCloseReportsUnconfirmedWriterWithoutQueueOverflow(t *testing.T) {
	writer := &blockedWriter{started: make(chan struct{}), release: make(chan struct{})}
	notice := noticeWriter{delivered: make(chan string, 1)}
	logger := newLogger(RoleCore, uuid.New(), writer, notice, Config{QueueSize: 4})
	logger.Application(ComponentCore).Info(string(ProcessStarted))
	<-writer.started
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if !errors.Is(logger.Close(ctx), context.DeadlineExceeded) {
		t.Fatal("expected bounded close error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("close exceeded bound: %v", elapsed)
	}
	status := logger.Status()
	if !status.Degraded || status.DroppedInfo != 0 || status.WriterErrors != 0 {
		t.Fatalf("unconfirmed close not distinguished: %+v", status)
	}
	select {
	case message := <-notice.delivered:
		if message != "GK80 diagnostic writer unavailable or records dropped\n" {
			t.Fatalf("unexpected notice: %q", message)
		}
	case <-time.After(time.Second):
		t.Fatal("fallback was not attempted")
	}
	close(writer.release)
	closeLogger(t, logger)
}

func TestCloseStaysBoundedWhenWriterAndFallbackBlock(t *testing.T) {
	writer := &blockedWriter{started: make(chan struct{}), release: make(chan struct{})}
	fallback := &blockedWriter{started: make(chan struct{}), release: make(chan struct{})}
	logger := newLogger(RoleCore, uuid.New(), writer, fallback, Config{QueueSize: 4})
	logger.Application(ComponentCore).Info(string(ProcessStarted))
	<-writer.started
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	if !errors.Is(logger.Close(ctx), context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatal("blocked fallback exceeded close bound")
	}
	select {
	case <-fallback.started:
	case <-time.After(time.Second):
		t.Fatal("safe fallback was not attempted")
	}
	close(fallback.release)
	close(writer.release)
	closeLogger(t, logger)
}

func TestCompletedWriterDoesNotWaitForUnneededFallback(t *testing.T) {
	done := make(chan struct{})
	close(done)
	logger := &Logger{queue: make(chan entry), done: done, fallback: make(chan struct{}, 1), fallbackStop: make(chan struct{}), fallbackDone: make(chan struct{}), degraded: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := logger.Close(ctx); err != nil || logger.Status().Degraded {
		t.Fatalf("completed writer reported unconfirmed while fallback was unnecessary: %v %+v", err, logger.Status())
	}
}
