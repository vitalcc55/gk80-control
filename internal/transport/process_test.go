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

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/profile"
	"github.com/vitalcc55/gk80-control/internal/storage"
	"go.yaml.in/yaml/v3"
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
	foundSQLite := false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event["event"] == "storage.ready" {
			version, ok := event["sqlite_version"].(string)
			foundSQLite = ok && version != "" && event["journal_mode"] == "wal" && event["synchronous"] == "FULL"
		}
	}
	if !foundSQLite {
		t.Fatal("confirmed SQLite version and settings absent from core log")
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

func TestCoreRecoversUnfinishedRecordWithoutResuming(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	command := contract.CreateTestCommand{CommandId: uuid.New(), Parameters: contract.TestParameters{Valve: "Демо", AreaM2: 0.01, PmaxPa: 1000, MeasurementSeconds: 1, Direction: "pressure"}}
	created, err := store.Create(ctx, command)
	if err != nil || created.TestId == nil {
		_ = store.Close()
		t.Fatalf("create: %v", err)
	}
	testID := *created.TestId
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	process, _, err := Start(ctx, buildCore(t), dir, "test", "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Close(ctx); err != nil {
		t.Fatal(err)
	}
	store, err = storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	record, err := store.Test(ctx, testID)
	if err != nil || record.Stage != "finished" || record.Outcome != "unknown" || record.DataQuality != "incomplete" {
		t.Fatalf("recovered: %+v %v", record, err)
	}
}

func TestCoreProcessLeaseAndSnapshot(t *testing.T) {
	executable := buildCore(t)
	profileDir := t.TempDir()
	process, handshake, err := Start(context.Background(), executable, profileDir, "test", "0.1.0")
	if process != nil {
		initial := process
		t.Cleanup(func() { _ = initial.Close(context.Background()) })
	}
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, bearer, lease string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, process.client.endpoint+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if lease != "" {
			req.Header.Set("X-GK80-UI-Lease", lease)
		}
		response, err := process.client.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		data, err := io.ReadAll(io.LimitReader(response.Body, 8193))
		if err != nil || len(data) > 8192 {
			t.Fatalf("response: %v", err)
		}
		return response.StatusCode, data
	}
	secret := process.client.secret
	if status, _ := request("GET", "/v1/snapshot", "", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("missing bearer: %d", status)
	}
	if status, _ := request("GET", "/v1/snapshot", "", secret, ""); status != http.StatusForbidden {
		t.Fatalf("missing lease: %d", status)
	}
	client := uuid.New()
	body := fmt.Sprintf(`{"clientId":%q}`, client.String())
	wrongMedia, err := http.NewRequest(http.MethodPost, process.client.endpoint+"/v1/ui-lease", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	wrongMedia.Header.Set("Authorization", "Bearer "+secret)
	wrongMedia.Header.Set("Content-Type", "text/plain")
	mediaResponse, err := process.client.http.Do(wrongMedia)
	if err != nil {
		t.Fatal(err)
	}
	_ = mediaResponse.Body.Close()
	if mediaResponse.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("unsupported media type: %d", mediaResponse.StatusCode)
	}
	ambiguousMedia, err := http.NewRequest(http.MethodPost, process.client.endpoint+"/v1/ui-lease", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	ambiguousMedia.Header.Set("Authorization", "Bearer "+secret)
	ambiguousMedia.Header.Add("Content-Type", "application/json")
	ambiguousMedia.Header.Add("Content-Type", "text/plain")
	mediaResponse, err = process.client.http.Do(ambiguousMedia)
	if err != nil {
		t.Fatal(err)
	}
	_ = mediaResponse.Body.Close()
	if mediaResponse.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("ambiguous media type: %d", mediaResponse.StatusCode)
	}
	for _, invalid := range []string{`{}`, `{"clientId":null}`, `{"clientId":"00000000-0000-0000-0000-000000000000"}`, fmt.Sprintf(`{"clientId":%q,"clientId":%q}`, client.String(), client.String())} {
		if status, _ := request("POST", "/v1/ui-lease", invalid, secret, ""); status != http.StatusBadRequest {
			t.Fatalf("invalid lease input: %d", status)
		}
	}
	status, data := request("POST", "/v1/ui-lease", body, secret, "")
	if status != http.StatusOK {
		t.Fatalf("acquire: %d %s", status, data)
	}
	var lease contract.UiLease
	if err := json.Unmarshal(data, &lease); err != nil || lease.ClientId != client || lease.LeaseId == uuid.Nil {
		t.Fatalf("lease: %s %v", data, err)
	}
	token := lease.LeaseId.String()
	if status, _ := request("GET", "/v1/snapshot", "", secret, "x"+token+"y"); status != http.StatusForbidden {
		t.Fatalf("malformed lease accepted: %d", status)
	}
	if status, _ := request("POST", "/v1/ui-lease", body, secret, ""); status != http.StatusConflict {
		t.Fatalf("same client without proof: %d", status)
	}
	if status, _ := request("POST", "/v1/ui-lease", body, secret, uuid.NewString()); status != http.StatusConflict {
		t.Fatalf("wrong proof: %d", status)
	}
	status, data = request("POST", "/v1/ui-lease", body, secret, token)
	var restored contract.UiLease
	if status != http.StatusOK || json.Unmarshal(data, &restored) != nil || restored != lease {
		t.Fatalf("restore: %d %s", status, data)
	}
	status, data = request("GET", "/v1/snapshot", "", secret, token)
	var snapshot contract.Snapshot
	if status != http.StatusOK || json.Unmarshal(data, &snapshot) != nil || snapshot.BackendRunId != handshake.BackendRunId || snapshot.ActiveTest != nil || snapshot.PendingAction != nil || snapshot.Measurements == nil || len(snapshot.Measurements) != 0 || snapshot.LastEventSequence != 0 {
		t.Fatalf("snapshot: %d %s", status, data)
	}
	schemaData, err := os.ReadFile("../../api/core.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var schemaDocument map[string]any
	if err := yaml.Unmarshal(schemaData, &schemaDocument); err != nil {
		t.Fatal(err)
	}
	schemaDocument["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://gk80.invalid/core.json", schemaDocument); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("https://gk80.invalid/core.json#/components/schemas/Snapshot")
	if err != nil {
		t.Fatal(err)
	}
	var actual any
	if err := json.Unmarshal(data, &actual); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(actual); err != nil {
		t.Fatalf("snapshot schema: %v", err)
	}
	if status, _ := request("PUT", "/v1/ui-lease", "", secret, token); status != http.StatusOK {
		t.Fatalf("renew: %d", status)
	}
	if status, _ := request("DELETE", "/v1/ui-lease", "", secret, token); status != http.StatusNoContent {
		t.Fatalf("release: %d", status)
	}
	if status, _ := request("GET", "/v1/snapshot", "", secret, token); status != http.StatusForbidden {
		t.Fatalf("revoked lease: %d", status)
	}
	if err := process.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	next, nextHandshake, err := Start(context.Background(), executable, profileDir, "test", "0.1.0")
	if next != nil {
		t.Cleanup(func() { _ = next.Close(context.Background()) })
	}
	if err != nil {
		t.Fatal(err)
	}
	process, handshake = next, nextHandshake
	if handshake.BackendRunId == snapshot.BackendRunId {
		t.Fatal("core restart reused run identity")
	}
	secret = process.client.secret
	if status, _ := request("GET", "/v1/snapshot", "", secret, token); status != http.StatusForbidden {
		t.Fatalf("previous run lease accepted: %d", status)
	}
}

func TestCoreProcessCreatesExactCommandAndRecoversLostResponse(t *testing.T) {
	ctx := context.Background()
	executable := buildCore(t)
	profileDir := t.TempDir()
	rejectedID := uuid.New()
	rejectedBody := fmt.Sprintf(`{"commandId":%q,"expectedRevision":0,"parameters":{"valve":"V","areaM2":0,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`, rejectedID.String())
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	var rejected contract.CreateTestCommand
	inspected, err := validator.Inspect("CreateTestCommand", []byte(rejectedBody), &rejected)
	if err != nil || inspected.Valid {
		t.Fatalf("rejection input: %v", err)
	}
	seed, err := storage.Open(profileDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Create(ctx, rejected); !errors.Is(err, storage.ErrInvalidCommand) {
		_ = seed.Close()
		t.Fatalf("seed rejection: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	process, _, err := Start(ctx, executable, profileDir, "test", "0.1.0")
	if process != nil {
		t.Cleanup(func() { _ = process.Close(ctx) })
	}
	if err != nil {
		t.Fatal(err)
	}
	leaseBody := fmt.Sprintf(`{"clientId":%q}`, uuid.NewString())
	request := func(method, path, body, lease string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, process.client.endpoint+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+process.client.secret)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if lease != "" {
			req.Header.Set("X-GK80-UI-Lease", lease)
		}
		response, err := process.client.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	decode := func(response *http.Response, target any) {
		t.Helper()
		defer func() { _ = response.Body.Close() }()
		if err := json.NewDecoder(io.LimitReader(response.Body, 8193)).Decode(target); err != nil {
			t.Fatal(err)
		}
	}
	response := request("POST", "/v1/ui-lease", leaseBody, "")
	var lease contract.UiLease
	if response.StatusCode != http.StatusOK {
		t.Fatalf("lease: %d", response.StatusCode)
	}
	decode(response, &lease)
	token := lease.LeaseId.String()
	response = request("POST", "/v1/tests", rejectedBody, token)
	var rejectedStatus contract.CommandStatus
	if response.StatusCode != http.StatusOK {
		t.Fatalf("known rejected command: %d", response.StatusCode)
	}
	decode(response, &rejectedStatus)
	if rejectedStatus.Status != "rejected" || rejectedStatus.CommandId != rejectedID {
		t.Fatalf("known rejection: %+v", rejectedStatus)
	}
	unknownID := uuid.New()
	unknownInvalid := strings.Replace(rejectedBody, rejectedID.String(), unknownID.String(), 1)
	response = request("POST", "/v1/tests", unknownInvalid, token)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown invalid command: %d", response.StatusCode)
	}
	response = request("GET", "/v1/commands/"+unknownID.String(), "", token)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("invalid command was persisted: %d", response.StatusCode)
	}
	response = request("POST", "/v1/tests", strings.Replace(rejectedBody, `"pmaxPa":1000`, `"pmaxPa":1001`, 1), token)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("invalid conflicting replay: %d", response.StatusCode)
	}
	commandID := uuid.New()
	body := fmt.Sprintf(`{"commandId":%q,"expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`, commandID.String())
	for _, invalid := range []string{
		strings.Replace(body, `,"actuator":false`, "", 1),
		strings.Replace(body, `"actuator":false`, `"actuator":null`, 1),
		strings.Replace(body, `"expectedRevision":0`, `"expectedRevision":0,"expectedRevision":0`, 1),
	} {
		response = request("POST", "/v1/tests", invalid, token)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid input: %d", response.StatusCode)
		}
	}
	response = request("POST", "/v1/tests", body, token)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("create: %d", response.StatusCode)
	}
	// The client loses the response after the server accepted the request.
	_ = response.Body.Close()
	response = request("GET", "/v1/commands/"+commandID.String(), "", token)
	var status contract.CommandStatus
	if response.StatusCode != http.StatusOK {
		t.Fatalf("command lookup: %d", response.StatusCode)
	}
	decode(response, &status)
	if status.Status != "completed" || status.TestId == nil {
		t.Fatalf("command: %+v", status)
	}
	testID := *status.TestId
	response = request("GET", "/v1/snapshot", "", token)
	var snapshot contract.Snapshot
	if response.StatusCode != http.StatusOK {
		t.Fatalf("snapshot: %d", response.StatusCode)
	}
	decode(response, &snapshot)
	if snapshot.ActiveTest == nil || snapshot.ActiveTest.TestId != testID {
		t.Fatalf("snapshot: %+v", snapshot)
	}
	response = request("GET", "/v1/tests/"+testID.String()+"/readiness", "", token)
	var readiness contract.Readiness
	if response.StatusCode != http.StatusOK {
		t.Fatalf("readiness: %d", response.StatusCode)
	}
	decode(response, &readiness)
	if !readiness.Ready {
		t.Fatalf("readiness: %+v", readiness)
	}
	equivalent := strings.Replace(body, `"areaM2":1,`, `"areaM2":1.00,`, 1)
	response = request("POST", "/v1/tests", equivalent, token)
	var repeated contract.CommandStatus
	if response.StatusCode != http.StatusOK {
		t.Fatalf("repeat: %d", response.StatusCode)
	}
	decode(response, &repeated)
	if repeated.TestId == nil || *repeated.TestId != testID {
		t.Fatalf("repeat: %+v", repeated)
	}
	conflicting := strings.Replace(body, `"areaM2":1,`, `"areaM2":1.0000000000000001,`, 1)
	response = request("POST", "/v1/tests", conflicting, token)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("identity conflict: %d", response.StatusCode)
	}
	revisionID := uuid.New()
	outdated := strings.Replace(body, commandID.String(), revisionID.String(), 1)
	response = request("POST", "/v1/tests", outdated, token)
	var rejectedRevision contract.CommandStatus
	if response.StatusCode != http.StatusOK {
		t.Fatalf("revision rejection: %d", response.StatusCode)
	}
	decode(response, &rejectedRevision)
	if rejectedRevision.Status != "rejected" || rejectedRevision.Reason == nil || *rejectedRevision.Reason != "revision" {
		t.Fatalf("revision rejection: %+v", rejectedRevision)
	}
	if err := process.Close(ctx); err != nil {
		t.Fatal(err)
	}
	logs, err := os.ReadDir(filepath.Join(profileDir, "logs"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("core logs: %v", err)
	}
	logData, err := os.ReadFile(filepath.Join(profileDir, "logs", logs[0].Name()))
	if err != nil || !strings.Contains(string(logData), commandID.String()) || !strings.Contains(string(logData), testID.String()) || !strings.Contains(string(logData), `"event":"command.completed"`) {
		t.Fatalf("missing command correlation: %v", err)
	}
	completed, conflict, revision := 0, false, false
	for _, line := range strings.Split(strings.TrimSpace(string(logData)), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event["command_id"] == commandID.String() && event["event"] == "command.completed" {
			completed++
		}
		if event["command_id"] == commandID.String() && event["event"] == "command.rejected" && event["error_code"] == "command_id_conflict" {
			conflict = true
		}
		if event["command_id"] == revisionID.String() && event["event"] == "command.rejected" && event["error_code"] == "revision" {
			revision = true
		}
	}
	if completed != 1 || !conflict || !revision {
		t.Fatalf("command diagnostics: completed=%d conflict=%t revision=%t", completed, conflict, revision)
	}
}
