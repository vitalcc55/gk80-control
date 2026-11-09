//go:build desktopapp

package desktop

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"testing"

	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestWailsBoundMethodUsesOriginalCommandDecoder(t *testing.T) {
	body := []byte(`{"commandId":"00000000-0000-4000-8000-000000000001","expectedRevision":0,"parameters":{"valve":"V","areaM2":0.100,"pmaxPa":1000,"measurementSeconds":1,"preOpenStabilizationSeconds":10,"postCloseStabilizationSeconds":10,"direction":"pressure","actuator":false}}`)
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
	calls     int
	input     CreateCommandInput
	exactBody string
}

func (c *bindingTestCore) CreateTestInput(_ context.Context, input CreateCommandInput) (contract.CommandStatus, error) {
	c.calls++
	c.input = input
	return contract.CommandStatus{CommandId: input.CommandId, Status: contract.CommandStatusStatusCompleted}, nil
}

func (c *bindingTestCore) LookupCommandExact(_ context.Context, _, _, _, _, _ string, body string) (contract.ExactLookupResult, error) {
	c.exactBody = body
	return contract.ExactLookupResult{Outcome: contract.ExactLookupResultOutcomeConflict}, nil
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
	invalid := json.RawMessage(`{"commandId":"00000000-0000-4000-8000-000000000001","parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":1,"preOpenStabilizationSeconds":10,"postCloseStabilizationSeconds":10,"direction":"pressure","actuator":false}}`)
	if _, err := method.Call(context.Background(), []json.RawMessage{invalid}); err == nil || core.calls != 0 {
		t.Fatal("registered Wails method accepted a missing revision")
	}
	valid := json.RawMessage(`{"commandId":"00000000-0000-4000-8000-000000000001","expectedRevision":0,"parameters":{"valve":"V","areaM2":0.100,"pmaxPa":1000,"measurementSeconds":1,"preOpenStabilizationSeconds":10,"postCloseStabilizationSeconds":10,"direction":"pressure","actuator":false}}`)
	if _, err := method.Call(context.Background(), []json.RawMessage{valid}); err != nil || core.calls != 1 || string(core.input.body) != string(valid) {
		t.Fatalf("registered Wails method lost original input: calls=%d err=%v", core.calls, err)
	}
	lookup := bindings.Get(&application.CallOptions{MethodName: "github.com/vitalcc55/gk80-control/internal/desktop.Service.LookupCommandExact"})
	if lookup == nil {
		t.Fatal("exact lookup method was not registered")
	}
	raw := `{"commandId":"00000000-0000-4000-8000-000000000001","expectedRevision":0,"parameters":{"valve":"V","areaM2":1.0000000000000001,"pmaxPa":1000,"measurementSeconds":1,"preOpenStabilizationSeconds":10,"postCloseStabilizationSeconds":10,"direction":"pressure","actuator":false}}`
	var lookupArgs []json.RawMessage
	for _, value := range []string{"00000000-0000-4000-8000-000000000004", "create", "", "", "00000000-0000-4000-8000-000000000001", raw} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		lookupArgs = append(lookupArgs, encoded)
	}
	if _, err := lookup.Call(context.Background(), lookupArgs); err != nil || core.exactBody != raw {
		t.Fatalf("Wails exact body: %v", err)
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
