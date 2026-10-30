// Package diagnostics provides bounded, non-authoritative process diagnostics.
package diagnostics

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Role string

const (
	RoleDesktop Role = "desktop"
	RoleCore    Role = "core"
)

type Event string

const (
	ProcessStarted      Event = "process.started"
	ProcessStopped      Event = "process.stopped"
	CoreStopped         Event = "core.stopped"
	CoreCleanupFailed   Event = "core.cleanup_failed"
	ConnectionFailed    Event = "connection.failed"
	ConnectionConfirmed Event = "connection.confirmed"
	DiagnosticDegraded  Event = "diagnostics.degraded"
	StorageReady        Event = "storage.ready"
	CommandAccepted     Event = "command.accepted"
	CommandRejected     Event = "command.rejected"
	CommandCompleted    Event = "command.completed"
	CommandFailed       Event = "command.failed"
	CommandObserved     Event = "command.observed"
	frameworkMessage    Event = "framework.message"
	libraryMessage      Event = "library.message"
)

type eventSpec struct {
	level slog.Level
	text  string
}

var eventCatalog = map[Event]eventSpec{
	ProcessStarted:      {slog.LevelInfo, "Process started"},
	ProcessStopped:      {slog.LevelInfo, "Process stopped"},
	CoreStopped:         {slog.LevelInfo, "Core stopped"},
	CoreCleanupFailed:   {slog.LevelError, "Core cleanup not confirmed"},
	ConnectionFailed:    {slog.LevelError, "Core connection failed"},
	ConnectionConfirmed: {slog.LevelInfo, "Core identity confirmed"},
	DiagnosticDegraded:  {slog.LevelWarn, "Diagnostic writer degraded"},
	StorageReady:        {slog.LevelInfo, "SQLite settings confirmed"},
	CommandAccepted:     {slog.LevelInfo, "Command accepted"},
	CommandRejected:     {slog.LevelInfo, "Command rejected"},
	CommandCompleted:    {slog.LevelInfo, "Command completed"},
	CommandFailed:       {slog.LevelError, "Command failed"},
	CommandObserved:     {slog.LevelInfo, "Command result observed"},
	frameworkMessage:    {slog.LevelInfo, "Framework diagnostic"},
	libraryMessage:      {slog.LevelInfo, "Library diagnostic"},
}

// Component identifies the trusted source of a diagnostic record.
type Component string

const (
	ComponentCore      Component = "core"
	ComponentDesktop   Component = "desktop"
	ComponentTransport Component = "transport"
	ComponentWails     Component = "wails"
	ComponentLibrary   Component = "library"
)

type Config struct {
	QueueSize  int
	MaxBytes   int64
	AppVersion string
	ProfileID  string
}

type Status struct {
	Degraded     bool
	WriterErrors uint64
	DroppedInfo  uint64
	DroppedWarn  uint64
	DroppedError uint64
}

// ErrCloseUnconfirmed means the diagnostic writer did not finish within its bound.
var ErrCloseUnconfirmed = errors.New("diagnostic writer shutdown unconfirmed")

type entry struct {
	data  []byte
	level slog.Level
}

// Logger owns one writer and a bounded, non-blocking queue for one process.
type Logger struct {
	role         Role
	runID        uuid.UUID
	writer       io.WriteCloser
	fallback     chan struct{}
	queue        chan entry
	done         chan struct{}
	fallbackDone chan struct{}
	fallbackStop chan struct{}
	stopFallback sync.Once
	degraded     chan struct{}
	maxBytes     int64
	appVersion   string
	profileID    string

	mu     sync.Mutex
	closed bool
	status Status
}

// Open creates a distinct file for this process run. On a file error it
// returns a degraded logger as well as the error so callers can keep running.
func Open(dir string, role Role, runID uuid.UUID, fallback io.Writer, config Config) (*Logger, error) {
	if (role != RoleCore && role != RoleDesktop) || runID == uuid.Nil || !validLabel(config.AppVersion) || !validLabel(config.ProfileID) {
		return nil, errors.New("invalid diagnostic process identity")
	}
	var writer io.WriteCloser
	err := os.MkdirAll(dir, 0o700)
	if err == nil {
		path := filepath.Join(dir, fmt.Sprintf("%s-%s.jsonl", role, runID))
		var file *os.File
		file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			writer = file
		}
	}
	logger := newLogger(role, runID, writer, fallback, config)
	if err != nil {
		logger.failWriter()
	}
	return logger, err
}

func validLabel(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' && char != '.' {
			return false
		}
	}
	return true
}

func newLogger(role Role, runID uuid.UUID, writer io.WriteCloser, fallback io.Writer, config Config) *Logger {
	if config.AppVersion == "" {
		config.AppVersion = "unknown"
	}
	if config.ProfileID == "" {
		config.ProfileID = "unknown"
	}
	if config.QueueSize <= 0 || config.QueueSize > 1024 {
		config.QueueSize = 128
	}
	if config.MaxBytes <= 0 || config.MaxBytes > 16*1024*1024 {
		config.MaxBytes = 16 * 1024 * 1024
	}
	if fallback == nil {
		fallback = io.Discard
	}
	logger := &Logger{
		role: role, runID: runID, writer: writer, maxBytes: config.MaxBytes, appVersion: config.AppVersion, profileID: config.ProfileID,
		queue: make(chan entry, config.QueueSize), done: make(chan struct{}), fallbackDone: make(chan struct{}), fallbackStop: make(chan struct{}), degraded: make(chan struct{}), fallback: make(chan struct{}, 1),
	}
	go func() {
		defer close(logger.fallbackDone)
		select {
		case <-logger.fallback:
			_, _ = io.WriteString(fallback, "GK80 diagnostic writer unavailable or records dropped\n")
		case <-logger.fallbackStop:
			select {
			case <-logger.fallback:
				_, _ = io.WriteString(fallback, "GK80 diagnostic writer unavailable or records dropped\n")
			default:
			}
		}
	}()
	go logger.writeLoop()
	return logger
}

type fields struct {
	command, test, action, backendRun       uuid.UUID
	errorCode                               string
	commandStatus                           string
	sqliteVersion, journalMode, synchronous string
	exitCode                                *int64
	grouped                                 bool
}

func (f *fields) add(attr slog.Attr) {
	if attr.Value.Kind() == slog.KindGroup {
		if len(attr.Value.Group()) > 0 {
			f.grouped = true
		}
		return
	}
	if attr.Value.Kind() == slog.KindAny {
		if value, ok := attr.Value.Any().(uuid.UUID); ok {
			f.add(slog.String(attr.Key, value.String()))
		}
		return
	}
	if attr.Value.Kind() == slog.KindString {
		value := attr.Value.String()
		switch attr.Key {
		case "command_id", "test_id", "action_id", "backend_run_id":
			parsed, err := uuid.Parse(value)
			if err != nil || parsed == uuid.Nil {
				return
			}
			switch attr.Key {
			case "command_id":
				f.command = parsed
			case "test_id":
				f.test = parsed
			case "action_id":
				f.action = parsed
			case "backend_run_id":
				f.backendRun = parsed
			}
		case "error_code":
			switch value {
			case "unavailable", "invalid_command", "command_id_conflict", "storage_unavailable", "revision", "active_test", "invalid_state", "action_mismatch", "test_not_found", "not_ready", "simulator_stop_failed":
				f.errorCode = value
			}
		case "command_status":
			switch value {
			case "accepted", "completed", "rejected", "failed", "unknown":
				f.commandStatus = value
			}
		case "sqlite_version":
			if len(value) > 0 && len(value) <= 32 {
				valid := true
				for _, c := range value {
					if (c < '0' || c > '9') && c != '.' {
						valid = false
						break
					}
				}
				if valid {
					f.sqliteVersion = value
				}
			}
		case "journal_mode":
			if value == "wal" {
				f.journalMode = value
			}
		case "synchronous":
			if value == "FULL" {
				f.synchronous = value
			}
		}
	}
	if attr.Key == "exit_code" && attr.Value.Kind() == slog.KindInt64 {
		value := attr.Value.Int64()
		if value >= -1 && value <= 4294967295 {
			f.exitCode = &value
		}
	}
}

func (l *Logger) emit(event Event, spec eventSpec, component Component, values fields) {
	record := slog.NewRecord(time.Now().UTC(), spec.level, spec.text, 0)
	record.AddAttrs(slog.Int("schema_version", 1), slog.String("event", string(event)), slog.String("component", string(component)), slog.String("process_role", string(l.role)), slog.String("process_run_id", l.runID.String()), slog.Int("pid", os.Getpid()), slog.String("app_version", l.appVersion), slog.String("profile_id", l.profileID), slog.String("mode", "demo"))
	if values.command != uuid.Nil {
		record.AddAttrs(slog.String("command_id", values.command.String()))
	}
	if values.test != uuid.Nil {
		record.AddAttrs(slog.String("test_id", values.test.String()))
	}
	if values.action != uuid.Nil {
		record.AddAttrs(slog.String("action_id", values.action.String()))
	}
	if values.backendRun != uuid.Nil {
		record.AddAttrs(slog.String("backend_run_id", values.backendRun.String()))
	}
	if values.errorCode != "" {
		record.AddAttrs(slog.String("error_code", values.errorCode))
	}
	if values.commandStatus != "" {
		record.AddAttrs(slog.String("command_status", values.commandStatus))
	}
	if values.sqliteVersion != "" {
		record.AddAttrs(slog.String("sqlite_version", values.sqliteVersion))
	}
	if values.journalMode != "" {
		record.AddAttrs(slog.String("journal_mode", values.journalMode))
	}
	if values.synchronous != "" {
		record.AddAttrs(slog.String("synchronous", values.synchronous))
	}
	if values.exitCode != nil {
		record.AddAttrs(slog.Int64("exit_code", *values.exitCode))
	}
	var buffer bytes.Buffer
	handler := slog.NewJSONHandler(&buffer, nil)
	if err := handler.Handle(context.Background(), record); err != nil || buffer.Len() > 4096 {
		l.drop(spec.level)
		return
	}
	item := entry{data: append([]byte(nil), buffer.Bytes()...), level: spec.level}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		l.dropLocked(spec.level)
		return
	}
	select {
	case l.queue <- item:
	default:
		l.dropLocked(spec.level)
	}
}

// Application exposes catalogued events through slog; unknown messages and fields are discarded.
func (l *Logger) Application(component Component) *slog.Logger {
	return slog.New(applicationHandler{logger: l, component: component})
}

type applicationHandler struct {
	logger    *Logger
	component Component
	values    fields
	grouped   bool
}

func (h applicationHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}
func (h applicationHandler) Handle(_ context.Context, record slog.Record) error {
	event := Event(record.Message)
	spec, ok := eventCatalog[event]
	if !ok || event == frameworkMessage || event == libraryMessage {
		h.logger.drop(record.Level)
		return nil
	}
	if record.Level > spec.level {
		spec.level = record.Level
	}
	if h.grouped || (h.component != ComponentCore && h.component != ComponentDesktop && h.component != ComponentTransport) {
		h.logger.drop(spec.level)
		return nil
	}
	values := h.values
	if !h.grouped {
		record.Attrs(func(attr slog.Attr) bool { values.add(attr); return true })
	}
	if values.grouped {
		h.logger.drop(spec.level)
		return nil
	}
	h.logger.emit(event, spec, h.component, values)
	return nil
}
func (h applicationHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if !h.grouped {
		for _, attr := range attrs {
			h.values.add(attr)
		}
	}
	return h
}
func (h applicationHandler) WithGroup(_ string) slog.Handler { h.grouped = true; return h }

// Framework and Library never record untrusted messages or fields.
func (l *Logger) Framework() *slog.Logger {
	return slog.New(frameworkHandler{logger: l, event: frameworkMessage, component: ComponentWails})
}

func (l *Logger) Library() *slog.Logger {
	return slog.New(frameworkHandler{logger: l, event: libraryMessage, component: ComponentLibrary})
}

type frameworkHandler struct {
	logger    *Logger
	event     Event
	component Component
}

func (h frameworkHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}
func (h frameworkHandler) Handle(_ context.Context, record slog.Record) error {
	h.logger.emit(h.event, eventSpec{level: record.Level, text: eventCatalog[h.event].text}, h.component, fields{})
	return nil
}
func (h frameworkHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h frameworkHandler) WithGroup(_ string) slog.Handler      { return h }

func (l *Logger) writeLoop() {
	defer close(l.done)
	var written int64
	broken := l.writer == nil
	for item := range l.queue {
		if broken || written+int64(len(item.data)) > l.maxBytes {
			if !broken {
				l.failWriter()
				broken = true
			}
			l.drop(item.level)
			continue
		}
		n, err := l.writer.Write(item.data)
		written += int64(n)
		if err != nil || n != len(item.data) {
			l.failWriter()
			l.drop(item.level)
			broken = true
		}
	}
	if l.writer != nil {
		if err := l.writer.Close(); err != nil {
			l.failWriter()
		}
	}
}

func (l *Logger) failWriter() {
	l.mu.Lock()
	l.status.WriterErrors++
	if !l.status.Degraded {
		close(l.degraded)
	}
	l.status.Degraded = true
	l.signalFallbackLocked()
	l.mu.Unlock()
}

func (l *Logger) drop(level slog.Level) {
	l.mu.Lock()
	l.dropLocked(level)
	l.mu.Unlock()
}

func (l *Logger) dropLocked(level slog.Level) {
	if !l.status.Degraded {
		close(l.degraded)
	}
	l.status.Degraded = true
	switch {
	case level >= slog.LevelError:
		l.status.DroppedError++
	case level >= slog.LevelWarn:
		l.status.DroppedWarn++
	default:
		l.status.DroppedInfo++
	}
	l.signalFallbackLocked()
}

func (l *Logger) signalFallbackLocked() {
	select {
	case l.fallback <- struct{}{}:
	default:
	}
}

func (l *Logger) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status
}

// Degraded closes on the first writer failure, dropped record or unconfirmed shutdown.
func (l *Logger) Degraded() <-chan struct{} { return l.degraded }

// ReportExternalDrops records losses from a bounded upstream diagnostic queue.
func (l *Logger) ReportExternalDrops(info, failed uint64) {
	if info == 0 && failed == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.status.Degraded {
		close(l.degraded)
	}
	l.status.Degraded = true
	l.status.DroppedInfo += info
	l.status.DroppedError += failed
	l.signalFallbackLocked()
}

// ReportExternalUnconfirmed signals that an upstream diagnostic stream may still be active.
func (l *Logger) ReportExternalUnconfirmed() { l.markUnconfirmed() }

func (l *Logger) markUnconfirmed() {
	l.mu.Lock()
	if !l.status.Degraded {
		close(l.degraded)
	}
	l.status.Degraded = true
	l.signalFallbackLocked()
	l.mu.Unlock()
}

// Close waits at most two seconds or until ctx is done; blocked I/O may outlive it.
func (l *Logger) Close(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		close(l.queue)
	}
	l.mu.Unlock()
	select {
	case <-l.done:
	case <-ctx.Done():
		select {
		case <-l.done:
		default:
			l.markUnconfirmed()
			l.stopFallback.Do(func() { close(l.fallbackStop) })
			return ctx.Err()
		}
	}
	l.stopFallback.Do(func() { close(l.fallbackStop) })
	if !l.Status().Degraded {
		return nil
	}
	select {
	case <-l.fallbackDone:
		return nil
	case <-ctx.Done():
		select {
		case <-l.fallbackDone:
			return nil
		default:
			l.markUnconfirmed()
			return ctx.Err()
		}
	}
}
