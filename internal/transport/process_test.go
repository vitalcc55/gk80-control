package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vitalcc55/gk80-control/internal/profile"
)

func init() {
	if os.Getenv("GK80_TEST_FAKE_CORE") != "cleanup" {
		return
	}
	reader := bufio.NewReaderSize(os.Stdin, 8193)
	var bootstrap Bootstrap
	if ReadFrame(reader, &bootstrap) != nil {
		os.Exit(2)
	}
	_ = json.NewEncoder(os.Stdout).Encode(Ready{ErrorCode: "test_rejection"})
	_, _ = io.Copy(io.Discard, reader)
	time.Sleep(2200 * time.Millisecond)
	if os.WriteFile(os.Getenv("GK80_TEST_CLEANUP_MARKER"), []byte("done"), 0o600) != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func buildCore(t *testing.T) string {
	t.Helper()
	name := "gk80-core"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", executable, "./cmd/gk80-core")
	cmd.Dir = filepath.Join("..", "..")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build core: %v\n%s", err, output)
	}
	return executable
}

func TestCoreProcessHandshakeAuthorizationAndProfileOwnership(t *testing.T) {
	executable := buildCore(t)
	profileDir := t.TempDir()
	process, handshake, err := Start(context.Background(), executable, profileDir, "test", "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close(context.Background()) })
	if process.PID() == os.Getpid() || !handshake.Demo || handshake.ApiVersion != "v1" {
		t.Fatal("unexpected core identity")
	}
	verifyExclusiveListener(t, process.ready.Address)
	for _, authorization := range []string{"", "Bearer wrong"} {
		req, err := http.NewRequest(http.MethodGet, process.client.endpoint+"/v1/handshake", nil)
		if err != nil {
			t.Fatal(err)
		}
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		response, err := process.client.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unexpected unauthorized status: %d", response.StatusCode)
		}
	}
	other, _, err := Start(context.Background(), executable, profileDir, "test", "0.1.0")
	if other != nil {
		_ = other.Close(context.Background())
	}
	if !errors.Is(err, ErrProfileInUse) {
		t.Fatalf("second core did not report occupied profile: %v", err)
	}
	if _, err := process.Handshake(context.Background()); err != nil {
		t.Fatalf("owner was interrupted: %v", err)
	}
	if err := process.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := process.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	logs, err := os.ReadDir(filepath.Join(profileDir, "logs"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("missing core log: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(profileDir, "logs", logs[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), process.client.secret) {
		t.Fatal("core secret leaked in diagnostics")
	}
	if !strings.Contains(string(data), handshake.BackendRunId.String()) {
		t.Fatal("core log is not correlated with run ID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reopened, _, err := Start(ctx, executable, profileDir, "test", "0.1.0")
	if err != nil {
		t.Fatalf("profile not released: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close(context.Background()) })
	if err := reopened.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reopened.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("killed core was not reaped")
	}
	if code, available := reopened.ExitCode(); !available || code == 0 {
		t.Fatalf("missing abnormal exit: %d %t", code, available)
	}
	_ = reopened.Close(context.Background())
	afterCrash, _, err := Start(context.Background(), executable, profileDir, "test", "0.1.0")
	if err != nil {
		t.Fatalf("profile not released after core crash: %v", err)
	}
	t.Cleanup(func() { _ = afterCrash.Close(context.Background()) })
	if err := afterCrash.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCoreDiagnosticFallbackReachesHostAsSafeSignal(t *testing.T) {
	executable := buildCore(t)
	profileDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(profileDir, "logs"), []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	process, _, err := Start(context.Background(), executable, profileDir, "test", "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close(context.Background()) })
	select {
	case <-process.DiagnosticsDegraded():
	case <-time.After(3 * time.Second):
		t.Fatal("core diagnostic failure was not observable")
	}
}

type noopCloser struct{}

func (noopCloser) Write(p []byte) (int, error) { return len(p), nil }
func (noopCloser) Close() error                { return nil }

func TestCloseAllowsCoreCleanupBudget(t *testing.T) {
	done := make(chan struct{})
	process := &Process{input: noopCloser{}, done: done}
	go func() { time.Sleep(2200 * time.Millisecond); close(done) }()
	if err := process.Close(context.Background()); err != nil {
		t.Fatalf("normal cleanup was interrupted: %v", err)
	}
}

func TestFailedStartupAllowsCoreCleanupBudget(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "cleanup-completed")
	t.Setenv("GK80_TEST_FAKE_CORE", "cleanup")
	t.Setenv("GK80_TEST_CLEANUP_MARKER", marker)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	process, _, err := Start(context.Background(), executable, t.TempDir(), "test", "0.1.0")
	if process != nil {
		_ = process.Close(context.Background())
	}
	if err == nil {
		t.Fatal("invalid bootstrap was accepted")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("failed startup interrupted core cleanup: %v", err)
	}
}

func TestCoreExitsAfterOwnerProcessDisappears(t *testing.T) {
	executable := buildCore(t)
	profileDir := t.TempDir()
	owner := exec.Command(os.Args[0], "-test.run=^TestOwnerExitHelper$")
	owner.Env = append(os.Environ(), "GK80_TEST_CORE="+executable, "GK80_TEST_PROFILE="+profileDir)
	input, err := owner.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := owner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	ownerDone := make(chan error, 1)
	waitStarted, waitReceived := false, false
	t.Cleanup(func() {
		if waitReceived {
			return
		}
		cleanupRequested := false
		if !waitStarted {
			cleanupRequested = true
			_, _ = io.WriteString(input, "cleanup\n")
			_ = input.Close()
			_ = output.Close()
			waitStarted = true
			go func() { ownerDone <- owner.Wait() }()
		}
		select {
		case waitErr := <-ownerDone:
			if waitErr != nil {
				if cleanupRequested {
					t.Errorf("owner could not confirm core cleanup: %v", waitErr)
				} else {
					t.Logf("owner cleanup: %v", waitErr)
				}
			}
		case <-time.After(10 * time.Second):
			_ = owner.Process.Kill()
			select {
			case waitErr := <-ownerDone:
				if waitErr != nil {
					t.Logf("owner cleanup: %v", waitErr)
				}
			case <-time.After(3 * time.Second):
				t.Error("owner cleanup could not be confirmed")
			}
		}
	})
	type lineResult struct {
		line string
		err  error
	}
	reader := bufio.NewReader(output)
	readLine := func() (string, error) {
		lineChannel := make(chan lineResult, 1)
		go func() { line, err := reader.ReadString('\n'); lineChannel <- lineResult{line, err} }()
		select {
		case result := <-lineChannel:
			return result.line, result.err
		case <-time.After(15 * time.Second):
			return "", errors.New("owner response timed out")
		}
	}
	line, err := readLine()
	if err != nil {
		t.Fatalf("owner did not report child: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("invalid child pid: %q", line)
	}
	observer, err := observeProcess(pid)
	if err != nil {
		t.Fatalf("cannot observe child: %v", err)
	}
	defer observer.Close()
	alive, err := observer.Alive()
	if err != nil || !alive {
		t.Fatalf("core was not running before owner exit: %v", err)
	}
	if _, err := io.WriteString(input, "verify\n"); err != nil {
		t.Fatal(err)
	}
	confirmation, err := readLine()
	if err != nil || confirmation != "verified\n" {
		t.Fatalf("core identity was not confirmed: %q %v", confirmation, err)
	}
	exited := false
	defer func() {
		if !exited {
			alive, err := observer.Alive()
			if err != nil {
				t.Errorf("cannot inspect core during cleanup: %v", err)
				return
			}
			var terminateErr error
			if alive {
				terminateErr = observer.Terminate()
			}
			if err := observer.Wait(); err != nil {
				t.Errorf("core cleanup could not be confirmed: %v (termination: %v)", err, terminateErr)
			}
		}
	}()
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	waitStarted = true
	go func() { ownerDone <- owner.Wait() }()
	select {
	case err := <-ownerDone:
		waitReceived = true
		if err != nil {
			t.Fatalf("owner helper failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("owner helper did not exit")
	}
	if err := observer.Wait(); err != nil {
		t.Fatalf("core survived owner exit: %v", err)
	}
	exited = true
	deadline := time.Now().Add(6 * time.Second)
	for {
		lock, err := profile.Acquire(profileDir)
		if err == nil {
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			break
		}
		if !errors.Is(err, profile.ErrInUse) || time.Now().After(deadline) {
			t.Fatalf("profile remained locked after owner exit: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestOwnerExitHelper(t *testing.T) {
	executable, profileDir := os.Getenv("GK80_TEST_CORE"), os.Getenv("GK80_TEST_PROFILE")
	if executable == "" || profileDir == "" {
		return
	}
	process, _, err := Start(context.Background(), executable, profileDir, "test", "0.1.0")
	if err != nil {
		if process != nil {
			if cleanupErr := process.Close(context.Background()); cleanupErr != nil {
				t.Errorf("core cleanup could not be confirmed: %v", cleanupErr)
			}
		}
		t.Fatal(err)
	}
	_, _ = fmt.Fprintln(os.Stdout, process.PID())
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	if line == "verify\n" {
		if _, err := process.Handshake(context.Background()); err != nil {
			_, _ = fmt.Fprintln(os.Stdout, "unverified")
		} else {
			_, _ = fmt.Fprintln(os.Stdout, "verified")
		}
		line, _ = reader.ReadString('\n')
	}
	if line == "cleanup\n" {
		if err := process.Close(context.Background()); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "owner could not confirm core cleanup")
			os.Exit(3)
		}
	}
	os.Exit(0)
}

func TestCoreDiagnosticExitIsObservableAndKeepsCoreFailureDistinct(t *testing.T) {
	for _, code := range []int{CoreExitDiagnostics, CoreExitFailureAndDiagnostics} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestDiagnosticExitHelper$")
		cmd.Env = append(os.Environ(), fmt.Sprintf("GK80_TEST_DIAGNOSTIC_EXIT=%d", code))
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		process := &Process{cmd: cmd, done: make(chan struct{}), diagnostics: &stderrSignal{notify: make(chan struct{})}}
		process.wait()
		select {
		case <-process.DiagnosticsDegraded():
		default:
			t.Fatal("diagnostic exit was not observed")
		}
		if (process.waitErr == nil) != (code == CoreExitDiagnostics) {
			t.Fatalf("core exit %d lost its distinction: %v", code, process.waitErr)
		}
	}
}

func TestDiagnosticExitHelper(t *testing.T) {
	value := os.Getenv("GK80_TEST_DIAGNOSTIC_EXIT")
	if value == "" {
		return
	}
	code, err := strconv.Atoi(value)
	if err != nil {
		os.Exit(1)
	}
	os.Exit(code)
}

func TestFailedStartRetainsDiagnosticSignal(t *testing.T) {
	done := make(chan struct{})
	close(done)
	signal := &stderrSignal{notify: make(chan struct{})}
	close(signal.notify)
	process := &Process{input: noopCloser{}, done: done, diagnostics: signal}
	startErr := errors.New("startup failed")
	retained, _, err := process.failedStart(startErr)
	if retained != process || !errors.Is(err, startErr) {
		t.Fatalf("diagnostic startup failure lost: %v", err)
	}
}
