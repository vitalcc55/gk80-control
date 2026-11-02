package browser

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

type exactRouteService struct {
	*routeService
	lookup func(context.Context, string, string, string, string, string, string) (contract.ExactLookupResult, error)
}

func (f *exactRouteService) LookupCommandExact(ctx context.Context, database, kind, test, action, command, body string) (contract.ExactLookupResult, error) {
	return f.lookup(ctx, database, kind, test, action, command, body)
}

func TestExactLookupRoutePreservesAttemptsAndRejectsInvalidRequests(t *testing.T) {
	database, command, testID, action := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	body := `{"commandId":"` + command + `","expectedRevision":0,"parameters":{"valve":"V","areaM2":1.0000000000000001,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	start := `{"commandId":"` + command + `","expectedRevision":0,"valveClosed":true,"connectionConfirmed":true}`
	confirm := `{"commandId":"` + command + `","expectedRevision":0}`
	var expectedKind, expectedTest, expectedAction, expectedBody string
	var calls int
	var mu sync.Mutex
	outcome := contract.ExactLookupResultOutcomeUnknown
	fake := &exactRouteService{routeService: &routeService{}, lookup: func(_ context.Context, db, kind, test, gotAction, id, original string) (contract.ExactLookupResult, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if db != database || kind != expectedKind || test != expectedTest || gotAction != expectedAction || id != command || original != expectedBody {
			t.Error("original lookup identity changed")
		}
		result := contract.ExactLookupResult{Outcome: outcome}
		if outcome == contract.ExactLookupResultOutcomeMatch {
			result.Command = &contract.CommandStatus{CommandId: uuid.MustParse(command), Status: contract.CommandStatusStatusCompleted}
		}
		return result, nil
	}}
	var router http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r) }))
	defer server.Close()
	s, code, err := newSession(server.Listener.Addr().String(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cookie, proof, err := s.pair(code)
	if err != nil {
		t.Fatal(err)
	}
	router = newRoutes(s, fake, fake)
	server.Start()
	request := func(kind, test, action, body string, mutate func(*http.Request)) (int, contract.ExactLookupResult) {
		t.Helper()
		r, err := http.NewRequest(http.MethodPost, server.URL+"/api/commands/"+command+"/lookup-exact", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.AddCookie(cookie)
		r.Header.Set("X-GK80-Session", proof)
		r.Header.Set("Origin", server.URL)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-GK80-Database", database)
		r.Header.Set("X-GK80-Kind", kind)
		if test != "" {
			r.Header.Set("X-GK80-Test", test)
		}
		if action != "" {
			r.Header.Set("X-GK80-Action", action)
		}
		if mutate != nil {
			mutate(r)
		}
		resp, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		var result contract.ExactLookupResult
		if resp.StatusCode == http.StatusOK {
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode, result
	}
	for _, tc := range []struct {
		kind, test, action, body string
		outcome                  contract.ExactLookupResultOutcome
	}{
		{"create", "", "", body, contract.ExactLookupResultOutcomeMatch},
		{"start", testID, "", start, contract.ExactLookupResultOutcomeUnknown},
		{"confirm", testID, action, confirm, contract.ExactLookupResultOutcomeConflict},
		{"create", "", "", body, contract.ExactLookupResultOutcomeDatabaseMismatch},
	} {
		mu.Lock()
		expectedKind, expectedTest, expectedAction, expectedBody, outcome = tc.kind, tc.test, tc.action, tc.body, tc.outcome
		mu.Unlock()
		status, result := request(tc.kind, tc.test, tc.action, tc.body, nil)
		if status != http.StatusOK || result.Outcome != tc.outcome {
			t.Fatalf("%s: %d %+v", tc.kind, status, result)
		}
	}
	paddedBody := body + strings.Repeat(" ", 8192-len(body))
	mu.Lock()
	expectedKind, expectedTest, expectedAction = "create", "", ""
	expectedBody = paddedBody
	outcome = contract.ExactLookupResultOutcomeUnknown
	mu.Unlock()
	if status, _ := request("create", "", "", paddedBody, nil); status != http.StatusOK {
		t.Fatalf("valid 8192-byte body: %d", status)
	}
	mu.Lock()
	before := calls
	mu.Unlock()
	for _, tc := range []struct {
		kind, test, action, body string
		mutate                   func(*http.Request)
	}{
		{"create", "", "", body, func(r *http.Request) { r.Header.Add("X-GK80-Database", database) }},
		{"create", testID, "", body, nil},
		{"confirm", testID, "", confirm, nil},
		{"start", testID, "", `{}`, nil},
		{"create", "", "", strings.Replace(body, command, uuid.NewString(), 1), nil},
		{"create", "", "", body + strings.Repeat(" ", 8193-len(body)), nil},
		{"create", "", "", body, func(r *http.Request) { r.URL.RawQuery = "x=1" }},
		{"create", "", "", body, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
	} {
		status, _ := request(tc.kind, tc.test, tc.action, tc.body, tc.mutate)
		if status != http.StatusBadRequest {
			t.Fatalf("invalid request accepted: %d", status)
		}
	}
	mu.Lock()
	afterInvalid := calls
	mu.Unlock()
	if afterInvalid != before {
		t.Fatal("invalid request reached service")
	}
	status, _ := request("create", "", "", body, func(r *http.Request) { r.Header.Del("X-GK80-Session") })
	mu.Lock()
	afterUnauthorized := calls
	mu.Unlock()
	if status != http.StatusUnauthorized || afterUnauthorized != before {
		t.Fatalf("unauthorized lookup: %d", status)
	}
}
