package transport

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

type Process struct {
	cmd         *exec.Cmd
	input       io.WriteCloser
	client      *Client
	ready       Ready
	done        chan struct{}
	waitErr     error
	closeOnce   sync.Once
	diagnostics *stderrSignal
}

var ErrProfileInUse = errors.New("demo profile is already in use")
var ErrCleanupUnconfirmed = errors.New("core process cleanup could not be confirmed")

// Core exit codes distinguish diagnostic shutdown from a failure of the core itself.
const CoreExitDiagnostics = 70
const CoreExitFailureAndDiagnostics = 71

// stderrSignal observes only the fixed diagnostics fallback, never raw child output.
type stderrSignal struct {
	mu       sync.Mutex
	line     []byte
	overflow bool
	once     sync.Once
	notify   chan struct{}
}

func (s *stderrSignal) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range data {
		if value == '\n' {
			if !s.overflow && string(s.line) == "GK80 diagnostic writer unavailable or records dropped" {
				s.once.Do(func() { close(s.notify) })
			}
			s.line = s.line[:0]
			s.overflow = false
		} else if !s.overflow {
			if len(s.line) == 256 {
				s.line = s.line[:0]
				s.overflow = true
			} else {
				s.line = append(s.line, value)
			}
		}
	}
	return len(data), nil
}

type readyResult struct {
	value Ready
	err   error
}

// Start owns exactly the child it launches and authenticates its passive handshake.
func Start(ctx context.Context, executable, profileDir, profileID, appVersion string) (*Process, contract.Handshake, error) {
	var empty contract.Handshake
	if !filepath.IsAbs(executable) || !filepath.IsAbs(profileDir) {
		return nil, empty, errors.New("absolute core and profile paths required")
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, empty, errors.New("core secret unavailable")
	}
	secret := base64.RawURLEncoding.EncodeToString(secretBytes)
	cmd := exec.Command(executable)
	configureCoreProcess(cmd)
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, empty, errors.New("core input unavailable")
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, empty, errors.New("core output unavailable")
	}
	stderr := &stderrSignal{notify: make(chan struct{})}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, empty, errors.New("core process unavailable")
	}
	process := &Process{cmd: cmd, input: input, done: make(chan struct{}), diagnostics: stderr}
	readyChannel := make(chan readyResult, 1)
	go func() {
		reader := bufio.NewReaderSize(output, 8193)
		var ready Ready
		err := ReadFrame(reader, &ready)
		readyChannel <- readyResult{value: ready, err: err}
		_, _ = io.Copy(io.Discard, reader)
		process.wait()
	}()
	fail := process.failedStart
	startupContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	bootstrap := Bootstrap{Secret: secret, ProfileDir: profileDir, ProfileID: profileID, AppVersion: appVersion}
	frame, err := json.Marshal(bootstrap)
	if err != nil || len(frame) > 8191 {
		return fail(errors.New("invalid core bootstrap configuration"))
	}
	frame = append(frame, '\n')
	written := make(chan error, 1)
	go func() {
		n, err := input.Write(frame)
		if err == nil && n != len(frame) {
			err = io.ErrShortWrite
		}
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			return fail(errors.New("core bootstrap failed"))
		}
	case <-startupContext.Done():
		return fail(errors.New("core bootstrap timed out"))
	}
	var ready Ready
	select {
	case result := <-readyChannel:
		if result.err != nil {
			return fail(errors.New("invalid core bootstrap"))
		}
		ready = result.value
	case <-startupContext.Done():
		return fail(errors.New("core bootstrap timed out"))
	}
	if ready.ErrorCode == "profile_in_use" {
		return fail(ErrProfileInUse)
	}
	if ready.ErrorCode != "" {
		return fail(errors.New("core bootstrap rejected"))
	}
	if ready.APIVersion != "v1" || !ready.Demo || ready.BackendRunID == uuid.Nil {
		return fail(errors.New("unsupported core bootstrap"))
	}
	decoded, err := hex.DecodeString(ready.Fingerprint)
	if err != nil || len(decoded) != 32 {
		return fail(errors.New("invalid core certificate fingerprint"))
	}
	var fingerprint [32]byte
	copy(fingerprint[:], decoded)
	client, err := NewClient(ready.Address, fingerprint, secret)
	if err != nil {
		return fail(err)
	}
	process.client = client
	process.ready = ready
	handshake, err := client.Handshake(startupContext)
	if err != nil || handshake.BackendRunId != ready.BackendRunID || handshake.ApiVersion != ready.APIVersion || !handshake.Demo {
		return fail(errors.New("core identity could not be confirmed"))
	}
	return process, handshake, nil
}

func (p *Process) failedStart(err error) (*Process, contract.Handshake, error) {
	_ = p.Close(context.Background())
	select {
	case <-p.done:
		select {
		case <-p.DiagnosticsDegraded():
			return p, contract.Handshake{}, err
		default:
			return nil, contract.Handshake{}, err
		}
	default:
		return p, contract.Handshake{}, ErrCleanupUnconfirmed
	}
}

func (p *Process) wait() {
	p.waitErr = p.cmd.Wait()
	if code := p.cmd.ProcessState.ExitCode(); code == CoreExitDiagnostics || code == CoreExitFailureAndDiagnostics {
		p.diagnostics.once.Do(func() { close(p.diagnostics.notify) })
		if code == CoreExitDiagnostics {
			p.waitErr = nil
		}
	}
	close(p.done)
}

func (p *Process) Handshake(ctx context.Context) (contract.Handshake, error) {
	result, err := p.client.Handshake(ctx)
	if err != nil {
		return contract.Handshake{}, err
	}
	if result.BackendRunId != p.ready.BackendRunID || result.ApiVersion != p.ready.APIVersion || !result.Demo {
		return contract.Handshake{}, errors.New("core identity changed")
	}
	return result, nil
}

func (p *Process) PID() int { return p.cmd.Process.Pid }

func (p *Process) Done() <-chan struct{} { return p.done }

func (p *Process) DiagnosticsDegraded() <-chan struct{} { return p.diagnostics.notify }

func (p *Process) ExitCode() (int, bool) {
	select {
	case <-p.done:
		return p.cmd.ProcessState.ExitCode(), true
	default:
		return 0, false
	}
}

// Close signals only this child, then waits; a timeout kills only that child.
func (p *Process) Close(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	p.closeOnce.Do(func() {
		if p.client != nil {
			p.client.Close()
		}
		go func() { _ = p.input.Close() }()
	})
	select {
	case <-p.done:
		return p.waitErr
	case <-ctx.Done():
		_ = p.cmd.Process.Kill()
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
			return ErrCleanupUnconfirmed
		}
		return ctx.Err()
	}
}
