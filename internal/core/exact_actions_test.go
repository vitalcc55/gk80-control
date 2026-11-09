package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/demo"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

func TestExactActionLookupUsesCommandAndPathIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine, err := demo.NewEngine(ctx, store, demo.NewSimulator(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	lease, err := leases.acquire(ctx, uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(ctx, contract.CreateTestCommand{CommandId: uuid.New(), ExpectedRevision: 0, Parameters: contract.TestParameters{PreOpenStabilizationSeconds: 10, PostCloseStabilizationSeconds: 10, Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 10, Direction: "pressure"}})
	if err != nil || created.TestId == nil {
		t.Fatalf("create setup: %+v %v", created, err)
	}
	testID, actionID := *created.TestId, uuid.New()
	start := contract.StartTestCommand{CommandId: uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := store.Start(ctx, testID, start, contract.PendingAction{TestId: testID, ActionId: actionID, Instruction: "confirm"}); err != nil {
		t.Fatalf("start setup: %v", err)
	}
	confirm := contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}
	if _, err := store.Confirm(ctx, testID, actionID, confirm); err != nil {
		t.Fatalf("confirm setup: %v", err)
	}
	rejected := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: false, ConnectionConfirmed: true}
	if _, err := store.RejectStartUnavailable(ctx, testID, rejected); err == nil {
		t.Fatal("invalid historical start was accepted")
	}
	large := contract.StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 9007199254740993, ValveClosed: true, ConnectionConfirmed: true}
	if _, err := store.RejectStartUnavailable(ctx, testID, large); err == nil {
		t.Fatal("historical unavailable start was accepted")
	}
	missingTestID := uuid.New()
	rejectedConfirm := contract.ConfirmActionCommand{CommandId: uuid.New(), ExpectedRevision: 2}
	if _, err := store.RejectConfirmUnavailable(ctx, missingTestID, actionID, rejectedConfirm); err == nil {
		t.Fatal("confirmation of missing test was accepted")
	}
	before, err := store.AuditPage(ctx, testID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	handler := &coreHTTP{secret: "secret", validator: validator, leases: leases, engine: engine, exact: store}
	request := func(path string, body any, database uuid.UUID) *httptest.ResponseRecorder {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(payload)))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set(leaseHeader, lease.id.String())
		req.Header.Set("X-GK80-Database-Id", database.String())
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(deadlineRecorder{response}, req)
		return response
	}
	startPath := "/v1/commands/lookup/start/" + testID.String()
	confirmPath := "/v1/commands/lookup/confirm/" + testID.String() + "/" + actionID.String()
	for _, tc := range []struct {
		path     string
		body     any
		id       uuid.UUID
		expected contract.CommandStatusStatus
	}{
		{startPath, start, start.CommandId, contract.CommandStatusStatusAccepted},
		{confirmPath, confirm, confirm.CommandId, contract.CommandStatusStatusCompleted},
		{startPath, rejected, rejected.CommandId, contract.CommandStatusStatusRejected},
		{startPath, large, large.CommandId, contract.CommandStatusStatusRejected},
		{"/v1/commands/lookup/confirm/" + missingTestID.String() + "/" + actionID.String(), rejectedConfirm, rejectedConfirm.CommandId, contract.CommandStatusStatusRejected},
	} {
		response := request(tc.path, tc.body, store.DatabaseID())
		var result contract.CommandStatus
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK || result.CommandId != tc.id || result.Status != tc.expected {
			t.Fatalf("match %s: %d %s %v", tc.path, response.Code, response.Body.String(), err)
		}
		if tc.id == rejectedConfirm.CommandId && (result.TestId != nil || result.Reason == nil || *result.Reason != "test_not_found") {
			t.Fatalf("missing test rejection: %+v", result)
		}
	}
	encodedStart, err := json.Marshal(start)
	if err != nil {
		t.Fatal(err)
	}
	equivalent := strings.Replace(string(encodedStart), `"expectedRevision":1`, `"expectedRevision":1.0`, 1)
	equivalent = strings.Replace(equivalent, start.CommandId.String(), strings.ToUpper(start.CommandId.String()), 1)
	if equivalent == string(encodedStart) || !strings.Contains(equivalent, `"expectedRevision":1.0`) || !strings.Contains(equivalent, strings.ToUpper(start.CommandId.String())) {
		t.Fatal("equivalent request did not change its representation")
	}
	response := request("/v1/commands/lookup/start/"+strings.ToUpper(testID.String()), json.RawMessage(equivalent), store.DatabaseID())
	var equivalentResult contract.CommandStatus
	if err := json.Unmarshal(response.Body.Bytes(), &equivalentResult); err != nil || response.Code != http.StatusOK || equivalentResult.CommandId != start.CommandId || equivalentResult.Status != contract.CommandStatusStatusAccepted || equivalentResult.TestId == nil || *equivalentResult.TestId != testID {
		t.Fatalf("equivalent start: %d %s %v", response.Code, response.Body.String(), err)
	}
	changedStart := start
	changedStart.ExpectedRevision++
	changedConfirm := confirm
	changedConfirm.ExpectedRevision++
	changedLarge := large
	changedLarge.ExpectedRevision--
	wrongOperation := start
	wrongOperation.CommandId = confirm.CommandId
	for _, tc := range []struct {
		path string
		body any
	}{
		{startPath, changedStart},
		{startPath, changedLarge},
		{startPath, wrongOperation},
		{"/v1/commands/lookup/start/" + uuid.NewString(), start},
		{confirmPath, changedConfirm},
		{"/v1/commands/lookup/confirm/" + testID.String() + "/" + uuid.NewString(), confirm},
		{"/v1/commands/lookup/confirm/" + uuid.NewString() + "/" + actionID.String(), confirm},
	} {
		if response := request(tc.path, tc.body, store.DatabaseID()); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"command_conflict"`) {
			t.Fatalf("conflict %s: %d %s", tc.path, response.Code, response.Body.String())
		}
	}
	missing := start
	missing.CommandId = uuid.New()
	response = request(startPath, missing, store.DatabaseID())
	var unknown contract.CommandStatus
	if err := json.Unmarshal(response.Body.Bytes(), &unknown); err != nil || response.Code != http.StatusOK || unknown.CommandId != missing.CommandId || unknown.Status != contract.CommandStatusStatusUnknown {
		t.Fatalf("unknown: %d %s %v", response.Code, response.Body.String(), err)
	}
	missingConfirm := confirm
	missingConfirm.CommandId = uuid.New()
	response = request(confirmPath, missingConfirm, store.DatabaseID())
	var unknownConfirm contract.CommandStatus
	if err := json.Unmarshal(response.Body.Bytes(), &unknownConfirm); err != nil || response.Code != http.StatusOK || unknownConfirm.CommandId != missingConfirm.CommandId || unknownConfirm.Status != contract.CommandStatusStatusUnknown {
		t.Fatalf("unknown confirm: %d %s %v", response.Code, response.Body.String(), err)
	}
	if response := request(startPath, start, uuid.New()); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"database_mismatch"`) {
		t.Fatalf("wrong database: %d %s", response.Code, response.Body.String())
	}
	if response := request(startPath, map[string]any{"commandId": start.CommandId}, store.DatabaseID()); response.Code != http.StatusBadRequest {
		t.Fatalf("missing fields: %d %s", response.Code, response.Body.String())
	}
	after, err := store.AuditPage(ctx, testID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("lookup changed audit: %+v %v", after, err)
	}
	if state := engine.State(); state.Record != nil {
		t.Fatalf("lookup changed engine: %+v", state.Record)
	}
}
