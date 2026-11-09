package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

func TestNativeExactLookupPreservesOriginalAndValidatesResponses(t *testing.T) {
	lease, database, commandID, testID, actionID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	create := `{"commandId":"` + commandID.String() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1.0000000000000001,"pmaxPa":1000,"measurementSeconds":10,"preOpenStabilizationSeconds":10,"postCloseStabilizationSeconds":10,"direction":"pressure","actuator":false}}`
	start := `{"commandId":"` + commandID.String() + `","expectedRevision":9007199254740993,"valveClosed":true,"connectionConfirmed":true}`
	confirm := `{"commandId":"` + commandID.String() + `","expectedRevision":2}`
	stop := `{"commandId":"` + commandID.String() + `"}`
	var calls atomic.Int32
	var expectedPath, expectedBody string
	var status int
	var response string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if r.Method != http.MethodPost || r.URL.Path != expectedPath || string(body) != expectedBody || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-GK80-UI-Lease") != lease.String() || r.Header.Get("X-GK80-Database-Id") != database.String() {
			t.Error("lookup did not preserve the expected route, identity or credentials")
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	defer server.Close()
	client := &Client{endpoint: server.URL, secret: "secret", http: server.Client()}
	for _, tc := range []struct {
		path, body string
		call       func() (contract.CommandStatus, error)
	}{
		{"/v1/commands/lookup/create", create, func() (contract.CommandStatus, error) {
			return client.LookupCreateExact(context.Background(), lease, database, commandID, []byte(create))
		}},
		{"/v1/commands/lookup/start/" + testID.String(), start, func() (contract.CommandStatus, error) {
			return client.LookupStartExact(context.Background(), lease, database, testID, commandID, []byte(start))
		}},
		{"/v1/commands/lookup/confirm/" + testID.String() + "/" + actionID.String(), confirm, func() (contract.CommandStatus, error) {
			return client.LookupConfirmExact(context.Background(), lease, database, testID, actionID, commandID, []byte(confirm))
		}},
		{"/v1/commands/lookup/stop/" + testID.String(), stop, func() (contract.CommandStatus, error) {
			return client.LookupStopExact(context.Background(), lease, database, testID, commandID, []byte(stop))
		}},
	} {
		expectedPath, expectedBody, status = tc.path, tc.body, http.StatusOK
		payload, _ := json.Marshal(contract.CommandStatus{CommandId: commandID, Status: contract.CommandStatusStatusUnknown})
		response = string(payload)
		got, err := tc.call()
		if err != nil || got.CommandId != commandID || got.Status != contract.CommandStatusStatusUnknown {
			t.Fatalf("%s: %+v %v", tc.path, got, err)
		}
	}
	expectedPath, expectedBody = "/v1/commands/lookup/start/"+testID.String(), start
	response = `{"commandId":"` + commandID.String() + `","status":"rejected","reason":"test_not_found"}`
	got, err := client.LookupStartExact(context.Background(), lease, database, testID, commandID, []byte(start))
	if err != nil || got.Status != contract.CommandStatusStatusRejected || got.TestId != nil {
		t.Fatalf("historical rejection: %+v %v", got, err)
	}
	other := uuid.New()
	response = `{"commandId":"` + commandID.String() + `","status":"completed","testId":"` + other.String() + `"}`
	if _, err := client.LookupStartExact(context.Background(), lease, database, testID, commandID, []byte(start)); err == nil {
		t.Fatal("different test ID accepted")
	}
	for _, code := range []string{"command_conflict", "database_mismatch"} {
		status, response = http.StatusConflict, `{"code":"`+code+`","message":"conflict"}`
		_, err := client.LookupStartExact(context.Background(), lease, database, testID, commandID, []byte(start))
		var failure *RequestError
		if !errors.As(err, &failure) || failure.Status != http.StatusConflict || failure.Code != code || failure.OutcomeUnknown {
			t.Fatalf("%s: %v", code, err)
		}
	}
	expectedPath, expectedBody = "/v1/commands/lookup/stop/"+testID.String(), stop
	for _, code := range []string{"command_conflict", "database_mismatch"} {
		status, response = http.StatusConflict, `{"code":"`+code+`","message":"conflict"}`
		_, err := client.LookupStopExact(context.Background(), lease, database, testID, commandID, []byte(stop))
		var failure *RequestError
		if !errors.As(err, &failure) || failure.Status != http.StatusConflict || failure.Code != code || failure.OutcomeUnknown {
			t.Fatalf("Stop %s: %v", code, err)
		}
	}
	status = http.StatusOK
	response = `{"commandId":"` + commandID.String() + `","status":"completed","testId":"` + uuid.NewString() + `"}`
	if _, err := client.LookupStopExact(context.Background(), lease, database, testID, commandID, []byte(stop)); err == nil {
		t.Fatal("Stop lookup accepted wrong test ID")
	}
	response = `{"commandId":"` + commandID.String() + `","status":"rejected"}`
	if _, err := client.LookupStopExact(context.Background(), lease, database, testID, commandID, []byte(stop)); err == nil {
		t.Fatal("Stop lookup accepted rejection without a test ID")
	}
	before := calls.Load()
	if _, err := client.LookupCreateExact(context.Background(), lease, database, uuid.New(), []byte(create)); err == nil || calls.Load() != before {
		t.Fatal("mismatched command ID was sent")
	}
	if _, err := client.LookupCreateExact(context.Background(), lease, database, commandID, []byte(strings.Repeat("x", 8193))); err == nil || calls.Load() != before {
		t.Fatal("oversized input was sent")
	}
	for _, invalid := range []struct {
		test, id uuid.UUID
		body     string
	}{
		{uuid.Nil, commandID, stop}, {testID, uuid.New(), stop}, {testID, commandID, `{}`},
		{testID, commandID, `{"commandId":"` + commandID.String() + `","extra":1}`},
		{testID, commandID, `{"commandId":"` + commandID.String() + `","commandId":"` + commandID.String() + `"}`},
	} {
		if _, err := client.LookupStopExact(context.Background(), lease, database, invalid.test, invalid.id, []byte(invalid.body)); err == nil || calls.Load() != before {
			t.Fatal("invalid Stop lookup was sent")
		}
	}
}

func TestNativeExactLookupRejectsUnconfirmedOrMismatchedReplies(t *testing.T) {
	lease, database, commandID, testID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	body := `{"commandId":"` + commandID.String() + `","expectedRevision":1,"valveClosed":true,"connectionConfirmed":true}`
	for _, tc := range []struct {
		name           string
		status         int
		response, code string
	}{
		{"wrong command", http.StatusOK, `{"commandId":"` + uuid.NewString() + `","status":"unknown"}`, "invalid_response"},
		{"missing command", http.StatusOK, `{"status":"unknown"}`, "invalid_response"},
		{"duplicate field", http.StatusOK, `{"commandId":"` + commandID.String() + `","status":"unknown","status":"completed"}`, "invalid_response"},
		{"success without test", http.StatusOK, `{"commandId":"` + commandID.String() + `","status":"completed"}`, "invalid_response"},
		{"unexpected success", http.StatusAccepted, `{"commandId":"` + commandID.String() + `","status":"unknown"}`, "invalid_response"},
		{"malformed conflict", http.StatusConflict, `{"code":"command_conflict"`, "invalid_response"},
		{"unavailable", http.StatusServiceUnavailable, `{"code":"state_unavailable","message":"unavailable"}`, "state_unavailable"},
		{"oversized", http.StatusOK, strings.Repeat("x", maxCoreResponseBytes+1), "invalid_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()
			client := &Client{endpoint: server.URL, secret: "secret", http: server.Client()}
			_, err := client.LookupStartExact(context.Background(), lease, database, testID, commandID, []byte(body))
			var failure *RequestError
			if !errors.As(err, &failure) || failure.Code != tc.code || failure.OutcomeUnknown || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), body) {
				t.Fatalf("unexpected lookup failure: %#v", err)
			}
		})
	}
}

func TestNativeExactLookupDoesNotRetryLostReadResponse(t *testing.T) {
	lease, database, commandID := uuid.New(), uuid.New(), uuid.New()
	body := `{"commandId":"` + commandID.String() + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":10,"preOpenStabilizationSeconds":10,"postCloseStabilizationSeconds":10,"direction":"pressure","actuator":false}}`
	var lookups, commands atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/commands/lookup/create" {
			lookups.Add(1)
		} else {
			commands.Add(1)
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	client := &Client{endpoint: server.URL, secret: "secret", http: server.Client()}
	_, err := client.LookupCreateExact(context.Background(), lease, database, commandID, []byte(body))
	var failure *RequestError
	if !errors.As(err, &failure) || failure.Code != "unavailable" || failure.OutcomeUnknown || lookups.Load() != 1 || commands.Load() != 0 {
		t.Fatalf("lost lookup response: %v lookups=%d commands=%d", err, lookups.Load(), commands.Load())
	}
}

func TestNativeExactStopLookupDoesNotRetryLostReadResponse(t *testing.T) {
	lease, database, commandID, testID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	body := `{"commandId":"` + commandID.String() + `"}`
	var lookups, commands atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/commands/lookup/stop/"+testID.String() {
			lookups.Add(1)
		} else {
			commands.Add(1)
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	client := &Client{endpoint: server.URL, secret: "secret", http: server.Client()}
	_, err := client.LookupStopExact(context.Background(), lease, database, testID, commandID, []byte(body))
	var failure *RequestError
	if !errors.As(err, &failure) || failure.Code != "unavailable" || failure.OutcomeUnknown || lookups.Load() != 1 || commands.Load() != 0 {
		t.Fatalf("lost Stop lookup response: %v lookups=%d actions=%d", err, lookups.Load(), commands.Load())
	}
}
