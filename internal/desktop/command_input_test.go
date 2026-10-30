package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/diagnostics"
	"github.com/vitalcc55/gk80-control/internal/transport"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestWailsCommandInputChecksOriginalJSON(t *testing.T) {
	id := `"00000000-0000-4000-8000-000000000001"`
	valid := `{"commandId":` + id + `,"expectedRevision":0,"parameters":{"valve":"V","areaM2":0.1,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	for name, body := range map[string]string{
		"missing revision": strings.Replace(valid, `,"expectedRevision":0`, "", 1),
		"missing actuator": strings.Replace(valid, `,"actuator":false`, "", 1),
		"extra property":   strings.Replace(valid, `"valve":"V"`, `"valve":"V","extra":1`, 1),
		"null boolean":     strings.Replace(valid, `"actuator":false`, `"actuator":null`, 1),
		"duplicate field":  strings.Replace(valid, `"actuator":false`, `"actuator":false,"actuator":false`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			var command CreateCommandInput
			if err := json.Unmarshal([]byte(body), &command); err == nil {
				t.Fatal("invalid original JSON was accepted")
			}
		})
	}
	var command CreateCommandInput
	if err := json.Unmarshal([]byte(valid), &command); err != nil || !command.lossless {
		t.Fatalf("valid decimal input: %+v %v", command, err)
	}
	var rounded CreateCommandInput
	if err := json.Unmarshal([]byte(strings.Replace(valid, `"areaM2":0.1`, `"areaM2":1.0000000000000001`, 1)), &rounded); err != nil || rounded.lossless {
		t.Fatalf("numeric loss was not detected: %+v %v", rounded, err)
	}
	var equivalent CreateCommandInput
	if err := json.Unmarshal([]byte(strings.Replace(valid, `"areaM2":0.1`, `"areaM2":0.100`, 1)), &equivalent); err != nil || string(equivalent.identity) != string(command.identity) {
		t.Fatalf("equivalent decimal identity: %v", err)
	}
	var invalidButStructured CreateCommandInput
	if err := json.Unmarshal([]byte(strings.Replace(valid, `"direction":"pressure"`, `"direction":"vacuum"`, 1)), &invalidButStructured); err != nil || invalidButStructured.valid {
		t.Fatalf("structured historical input was discarded before core lookup: %v", err)
	}
}

func TestWailsBoundMethodUsesOriginalCommandDecoder(t *testing.T) {
	body := []byte(`{"commandId":"00000000-0000-4000-8000-000000000001","expectedRevision":0,"parameters":{"valve":"V","areaM2":0.100,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`)
	method := &application.BoundMethod{
		Method: reflect.ValueOf(func(input CreateCommandInput) bool {
			return string(input.body) == string(body) && input.lossless
		}),
		Inputs: []*application.Parameter{{ReflectType: reflect.TypeOf(CreateCommandInput{})}},
	}
	result, err := method.Call(context.Background(), []json.RawMessage{body})
	if err != nil || result != true {
		t.Fatalf("Wails argument decoding: %v %v", result, err)
	}
}

type bindingTestCore struct {
	Core
	calls int
	input CreateCommandInput
}

func (c *bindingTestCore) CreateTestInput(_ context.Context, input CreateCommandInput) (contract.CommandStatus, error) {
	c.calls++
	c.input = input
	return contract.CommandStatus{CommandId: input.CommandId, Status: contract.CommandStatusStatusCompleted}, nil
}

func TestWailsRegisteredBindingRejectsMissingFields(t *testing.T) {
	const childEnv = "GK80_WAILS_BINDING_TEST_CHILD"
	if os.Getenv(childEnv) != "1" {
		process := exec.Command(os.Args[0], "-test.run=^TestWailsRegisteredBindingRejectsMissingFields$")
		process.Env = append(os.Environ(), childEnv+"=1")
		if output, err := process.CombinedOutput(); err != nil {
			t.Fatalf("binding subprocess: %v %s", err, output)
		}
		return
	}
	_ = application.New(application.Options{})
	core := &bindingTestCore{}
	bindings := application.NewBindings(nil, nil)
	if err := bindings.Add(NewWailsService(core)); err != nil {
		t.Fatal(err)
	}
	method := bindings.Get(&application.CallOptions{MethodName: "github.com/vitalcc55/gk80-control/internal/desktop.Service.CreateTest"})
	if method == nil {
		t.Fatal("create method was not registered")
	}
	invalid := json.RawMessage(`{"commandId":"00000000-0000-4000-8000-000000000001","parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`)
	if _, err := method.Call(context.Background(), []json.RawMessage{invalid}); err == nil || core.calls != 0 {
		t.Fatal("registered Wails method accepted a missing revision")
	}
	valid := json.RawMessage(`{"commandId":"00000000-0000-4000-8000-000000000001","expectedRevision":0,"parameters":{"valve":"V","areaM2":0.100,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`)
	if _, err := method.Call(context.Background(), []json.RawMessage{valid}); err != nil || core.calls != 1 || string(core.input.body) != string(valid) {
		t.Fatalf("registered Wails method lost original input: calls=%d err=%v", core.calls, err)
	}
	for _, tc := range []struct {
		name string
		args []json.RawMessage
	}{
		{name: "StartTest", args: []json.RawMessage{json.RawMessage(`"00000000-0000-4000-8000-000000000002"`), json.RawMessage(`{"commandId":"00000000-0000-4000-8000-000000000001","expectedRevision":0,"valveClosed":true}`)}},
		{name: "ConfirmAction", args: []json.RawMessage{json.RawMessage(`"00000000-0000-4000-8000-000000000002"`), json.RawMessage(`"00000000-0000-4000-8000-000000000003"`), json.RawMessage(`{"commandId":"00000000-0000-4000-8000-000000000001"}`)}},
		{name: "StopTest", args: []json.RawMessage{json.RawMessage(`"00000000-0000-4000-8000-000000000002"`), json.RawMessage(`{}`)}},
	} {
		bound := bindings.Get(&application.CallOptions{MethodName: "github.com/vitalcc55/gk80-control/internal/desktop.Service." + tc.name})
		if bound == nil {
			t.Fatalf("method not registered: %s", tc.name)
		}
		if _, err := bound.Call(context.Background(), tc.args); err == nil {
			t.Fatalf("%s accepted missing fields", tc.name)
		}
	}
}

func TestWailsCreateRejectsNewRoundedValueButForwardsHistoricalReplay(t *testing.T) {
	run, lease, id := uuid.New(), uuid.New(), uuid.New()
	body := fmt.Sprintf(`{"commandId":%q,"expectedRevision":0,"parameters":{"valve":"V","areaM2":1.0000000000000001,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`, id)
	var input CreateCommandInput
	if err := json.Unmarshal([]byte(body), &input); err != nil {
		t.Fatal(err)
	}
	sends := 0
	client := &testSessionClient{}
	client.createRaw = func(_ context.Context, gotLease, gotID uuid.UUID, raw []byte) (contract.CommandStatus, error) {
		sends++
		if gotLease != lease || gotID != id || !strings.Contains(string(raw), `1.0000000000000001`) {
			t.Error("original create request was not preserved")
		}
		return contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatusCompleted}, nil
	}
	session := newDemoSession(context.Background(), client, uuid.New(), run)
	defer session.close()
	session.mu.Lock()
	session.leaseID, session.leaseConfirmed = lease, true
	session.leaseUntil = time.Now().Add(time.Minute)
	session.lastUI, session.lastProbe = time.Now(), time.Now()
	session.streamActive = true
	session.streamDone = make(chan struct{})
	session.stream = &testSessionStream{events: make(chan struct{}, 1), done: make(chan struct{})}
	session.snapshot = &contract.Snapshot{BackendRunId: run, Measurements: []contract.Measurement{}}
	session.mu.Unlock()
	owner := &Owner{session: session, status: contract.DesktopStatus{Status: contract.DesktopStatusStatusReady}}
	if _, err := owner.CreateTestInput(context.Background(), input); err == nil || sends != 0 {
		t.Fatal("new rounded decimal was not rejected before execution")
	}
	client.mu.Lock()
	client.command = contract.CommandStatus{CommandId: id, Status: contract.CommandStatusStatusCompleted}
	client.mu.Unlock()
	status, err := owner.CreateTestInput(context.Background(), input)
	if err != nil || status.Status != contract.CommandStatusStatusCompleted || sends != 1 {
		t.Fatalf("historical replay: %+v %v, sends=%d", status, err, sends)
	}
}

func TestWailsActionInputsRequireOriginalFields(t *testing.T) {
	for name, value := range map[string]any{
		"start":   &StartCommandInput{},
		"confirm": &ConfirmCommandInput{},
		"stop":    &StopCommandInput{},
	} {
		t.Run(name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(`{}`), value); err == nil {
				t.Fatal("missing command fields were accepted")
			}
		})
	}
}

func TestForwardCommandLogsSafeRejectionReasons(t *testing.T) {
	dir, hostID, runID, commandID := t.TempDir(), uuid.New(), uuid.New(), uuid.New()
	logger, err := diagnostics.Open(dir, diagnostics.RoleDesktop, hostID, io.Discard, diagnostics.Config{QueueSize: 32, MaxBytes: 1 << 20, AppVersion: "0.1.0", ProfileID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	owner := &Owner{log: logger.Application(diagnostics.ComponentTransport)}
	session := &demoSession{backendID: runID}
	_, _ = owner.forwardCommand(context.Background(), session, commandID, uuid.Nil, uuid.Nil, func(context.Context) (contract.CommandStatus, error) {
		return contract.CommandStatus{}, &transport.RequestError{Status: 409, Code: "command_conflict"}
	})
	reason := "revision"
	_, _ = owner.forwardCommand(context.Background(), session, commandID, uuid.Nil, uuid.Nil, func(context.Context) (contract.CommandStatus, error) {
		return contract.CommandStatus{CommandId: commandID, Status: contract.CommandStatusStatusRejected, Reason: &reason}, nil
	})
	_, _ = owner.forwardCommand(context.Background(), session, commandID, uuid.Nil, uuid.Nil, func(context.Context) (contract.CommandStatus, error) {
		return contract.CommandStatus{}, &transport.RequestError{Status: 404, Code: "not_found"}
	})
	for _, code := range []string{"unavailable", "invalid_response", "lease_invalid", "state_unavailable", "outcome_unknown"} {
		_, _ = owner.forwardCommand(context.Background(), session, commandID, uuid.Nil, uuid.Nil, func(context.Context) (contract.CommandStatus, error) {
			return contract.CommandStatus{}, &transport.RequestError{Code: code, OutcomeUnknown: true}
		})
	}
	if err := logger.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "desktop-"+hostID.String()+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var conflict, rejected, notFound bool
	unknown := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["command_id"] != commandID.String() || record["backend_run_id"] != runID.String() {
			t.Fatalf("missing correlation: %s", line)
		}
		conflict = conflict || record["event"] == "command.rejected" && record["error_code"] == "command_id_conflict"
		rejected = rejected || record["event"] == "command.observed" && record["command_status"] == "rejected" && record["error_code"] == "revision"
		notFound = notFound || record["event"] == "command.rejected" && record["error_code"] == "not_found"
		if record["event"] == "command.observed" && record["command_status"] == "unknown" {
			if code, ok := record["error_code"].(string); ok {
				unknown[code] = true
			}
		}
	}
	if !conflict || !rejected || !notFound || len(unknown) != 5 {
		t.Fatalf("rejection reasons missing: %s", data)
	}
}
