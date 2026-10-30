package core

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/demo"
	"github.com/vitalcc55/gk80-control/internal/profile"
	"github.com/vitalcc55/gk80-control/internal/storage"
	"github.com/vitalcc55/gk80-control/internal/transport"
	"go.yaml.in/yaml/v3"
)

type heldMeasurementWrite struct {
	*storage.Store
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *heldMeasurementWrite) RecordMeasurements(ctx context.Context, id uuid.UUID, values []storage.TimedMeasurement) error {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.Store.RecordMeasurements(ctx, id, values)
}

func TestCoreShutdownRetainsResourcesForBlockedWriter(t *testing.T) {
	dir := t.TempDir()
	input, owner := io.Pipe()
	ready, output := io.Pipe()
	held := &heldMeasurementWrite{entered: make(chan struct{}), release: make(chan struct{})}
	port := &countedEquipment{Equipment: demo.NewSimulator()}
	engineReady := make(chan *demo.Engine, 1)
	runDone := make(chan error, 1)
	var releaseOnce sync.Once
	var engine *demo.Engine
	runFinished := false
	defer func() {
		releaseOnce.Do(func() { close(held.release) })
		_ = owner.Close()
		_ = ready.Close()
		if !runFinished {
			select {
			case err := <-runDone:
				if err != nil {
					t.Logf("core cleanup: %v", err)
				}
				runFinished = true
			case <-time.After(7 * time.Second):
				t.Error("core did not finish during cleanup")
				return
			}
		}
		if engine == nil {
			select {
			case engine = <-engineReady:
			default:
			}
		}
		if engine != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := engine.Shutdown(ctx, "owner_lost")
			cancel()
			if err != nil {
				t.Errorf("engine cleanup unconfirmed: %v", err)
				return
			}
		}
		retainedCoreResources.Lock()
		var lock *profile.Lock
		for index, item := range retainedCoreResources.items {
			if item.store == held.Store {
				lock = item.lock
				retainedCoreResources.items = append(retainedCoreResources.items[:index], retainedCoreResources.items[index+1:]...)
				break
			}
		}
		retainedCoreResources.Unlock()
		if lock != nil {
			_ = held.Close()
			_ = lock.Close()
		}
	}()
	go func() {
		defer func() { _ = output.Close() }()
		runDone <- run(context.Background(), input, output, io.Discard, func(ctx context.Context, store *storage.Store, id uuid.UUID) (*demo.Engine, error) {
			held.Store = store
			engine, err := demo.NewEngine(ctx, held, port, id)
			if err == nil {
				engineReady <- engine
			}
			return engine, err
		})
	}()
	bootstrap := transport.Bootstrap{Secret: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ProfileDir: dir, ProfileID: "test", AppVersion: "test"}
	if err := json.NewEncoder(owner).Encode(bootstrap); err != nil {
		t.Fatal(err)
	}
	var response transport.Ready
	if err := json.NewDecoder(ready).Decode(&response); err != nil || !response.Demo {
		t.Fatalf("ready: %+v %v", response, err)
	}
	engine = <-engineReady
	fingerprint, err := hex.DecodeString(response.Fingerprint)
	if err != nil || len(fingerprint) != sha256.Size {
		t.Fatal("invalid fingerprint")
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // The test pins the bootstrap fingerprint.
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("missing certificate")
			}
			actual := sha256.Sum256(state.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(actual[:], fingerprint) != 1 {
				return errors.New("certificate mismatch")
			}
			return nil
		},
	}}}
	defer client.CloseIdleConnections()
	send := func(method, path, body, lease string, target any) int {
		t.Helper()
		req, err := http.NewRequest(method, "https://"+response.Address+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bootstrap.Secret)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if lease != "" {
			req.Header.Set(leaseHeader, lease)
		}
		result, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = result.Body.Close() }()
		if target != nil {
			if err := json.NewDecoder(io.LimitReader(result.Body, 8193)).Decode(target); err != nil {
				t.Fatal(err)
			}
		}
		return result.StatusCode
	}
	var lease contract.UiLease
	if code := send("POST", "/v1/ui-lease", fmt.Sprintf(`{"clientId":%q}`, uuid.NewString()), "", &lease); code != http.StatusOK {
		t.Fatalf("lease: %d", code)
	}
	token := lease.LeaseId.String()
	var created contract.CommandStatus
	body := fmt.Sprintf(`{"commandId":%q,"expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":10,"direction":"pressure","actuator":false}}`, uuid.NewString())
	if code := send("POST", "/v1/tests", body, token, &created); code != http.StatusOK || created.TestId == nil {
		t.Fatalf("create: %d %+v", code, created)
	}
	id := *created.TestId
	start := fmt.Sprintf(`{"commandId":%q,"expectedRevision":1,"valveClosed":true,"connectionConfirmed":true}`, uuid.NewString())
	var accepted contract.CommandAccepted
	if code := send("POST", "/v1/tests/"+id.String()+"/start", start, token, &accepted); code != http.StatusAccepted {
		t.Fatalf("start: %d", code)
	}
	var snapshot contract.Snapshot
	if code := send("GET", "/v1/snapshot", "", token, &snapshot); code != http.StatusOK || snapshot.PendingAction == nil {
		t.Fatalf("snapshot: %d %+v", code, snapshot)
	}
	confirm := fmt.Sprintf(`{"commandId":%q,"expectedRevision":%d}`, uuid.NewString(), snapshot.Revision)
	var confirmed contract.CommandStatus
	path := "/v1/tests/" + id.String() + "/actions/" + snapshot.PendingAction.ActionId.String() + "/confirm"
	if code := send("POST", path, confirm, token, &confirmed); code != http.StatusOK {
		t.Fatalf("confirm: %d", code)
	}
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not start")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-runDone:
		runFinished = true
		if err == nil {
			t.Fatal("unconfirmed shutdown reported success")
		}
	case <-time.After(7 * time.Second):
		t.Fatal("core shutdown exceeded its bound")
	}
	state := engine.State()
	if port.stops.Load() != 1 || state.Stop == nil || state.Stop.Status != "completed" || state.Record.SaveStatus == "saved" {
		t.Fatalf("stop or save status: %+v", state)
	}
	runtime.GC()
	if other, err := profile.Acquire(dir); !errors.Is(err, profile.ErrInUse) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatalf("profile lock released: %v", err)
	}
	if _, err := held.Revision(context.Background()); err != nil {
		t.Fatalf("store closed while writer is active: %v", err)
	}
	releaseOnce.Do(func() { close(held.release) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx, "owner_lost"); err != nil {
		t.Fatalf("writer recovery: %v", err)
	}
	runtime.GC()
	if other, err := profile.Acquire(dir); !errors.Is(err, profile.ErrInUse) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatalf("profile lock released after writer returned: %v", err)
	}
	if _, err := held.Revision(context.Background()); err != nil {
		t.Fatalf("store closed after writer returned: %v", err)
	}
}

func TestHandshakeErrorsMatchContract(t *testing.T) {
	data, err := os.ReadFile("../../api/core.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("https://gk80.invalid/core.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("https://gk80.invalid/core.json#/components/schemas/ApiError")
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	databaseID := store.DatabaseID()
	handler := handshakeHandler("known-secret", uuid.New(), databaseID)
	for _, authorization := range []string{"", "Bearer wrong", "wrong", "Bearer known-secret-extra"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/handshake", nil)
		req.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusUnauthorized || response.Header().Get("Content-Type") != "application/json" || response.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("unauthorized response: %d %v", response.Code, response.Header())
		}
		var body contract.ApiError
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Code != "unauthorized" || body.Message != "Authentication required" {
			t.Fatalf("invalid response: %s: %v", response.Body.String(), err)
		}
		var actual any
		if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(actual); err != nil {
			t.Fatalf("response does not match ApiError: %v", err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/handshake", nil)
	req.Header.Set("Authorization", "Bearer known-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET" {
		t.Fatalf("method response: %d %v", response.Code, response.Header())
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/handshake", nil)
	req.Header.Set("Authorization", "Bearer known-secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	var handshake contract.Handshake
	if err := json.Unmarshal(response.Body.Bytes(), &handshake); err != nil || response.Code != http.StatusOK || handshake.DatabaseId != databaseID {
		t.Fatalf("authorized identity: %d %+v %v", response.Code, handshake, err)
	}
}
