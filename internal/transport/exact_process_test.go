package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

// droppedCreateResponse retains the pinned transport and loses one completed response.
type droppedCreateResponse struct {
	next     http.RoundTripper
	posts    *atomic.Int32
	enabled  bool
	selected atomic.Bool
	dropped  atomic.Bool
}

func (d *droppedCreateResponse) RoundTrip(req *http.Request) (*http.Response, error) {
	create := req.Method == http.MethodPost && req.URL.Path == "/v1/tests"
	if create {
		d.posts.Add(1)
	}
	response, err := d.next.RoundTrip(req)
	if err != nil || !create || !d.enabled || response.StatusCode != http.StatusOK || !d.selected.CompareAndSwap(false, true) {
		return response, err
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	d.dropped.Store(true)
	return nil, errors.New("test dropped completed create response")
}

func (d *droppedCreateResponse) CloseIdleConnections() {
	if closer, ok := d.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// droppedStopResponse loses the first accepted Stop response, after core handled it.
type droppedStopResponse struct {
	next     http.RoundTripper
	path     string
	posts    *atomic.Int32
	enabled  bool
	selected atomic.Bool
	dropped  atomic.Bool
}

func (d *droppedStopResponse) RoundTrip(req *http.Request) (*http.Response, error) {
	stop := req.Method == http.MethodPost && req.URL.Path == d.path
	if stop {
		d.posts.Add(1)
	}
	response, err := d.next.RoundTrip(req)
	if err != nil || !stop || !d.enabled || response.StatusCode != http.StatusAccepted || !d.selected.CompareAndSwap(false, true) {
		return response, err
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	d.dropped.Store(true)
	return nil, errors.New("test dropped accepted Stop response")
}

func (d *droppedStopResponse) CloseIdleConnections() {
	if closer, ok := d.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func TestNativeExactLookupAfterLostCreateAndReopen(t *testing.T) {
	ctx := context.Background()
	profile, executable := t.TempDir(), buildCore(t)
	process, handshake, err := Start(ctx, executable, profile, "test", "0.1.0")
	if process != nil {
		t.Cleanup(func() { _ = process.Close(ctx) })
	}
	if err != nil {
		t.Fatal(err)
	}
	client := process.Client()
	lease, err := client.AcquireLease(ctx, uuid.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	drop := &droppedCreateResponse{next: client.http.Transport, posts: &posts, enabled: true}
	client.http.Transport = drop
	command := contract.CreateTestCommand{CommandId: uuid.New(), ExpectedRevision: 0, Parameters: contract.TestParameters{PreOpenStabilizationSeconds: 10, PostCloseStabilizationSeconds: 10, Valve: "V", AreaM2: 1, PmaxPa: 1000, MeasurementSeconds: 60, Direction: "pressure"}}
	body, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"areaM2":1`) {
		t.Fatal("unexpected create input")
	}
	_, err = client.CreateTestRaw(ctx, lease.LeaseId, command.CommandId, body)
	var lost *RequestError
	if !errors.As(err, &lost) || !lost.OutcomeUnknown || !drop.dropped.Load() || drop.posts.Load() != 1 {
		t.Fatalf("lost response: %v posts=%d", err, drop.posts.Load())
	}
	status, err := client.LookupCreateExact(ctx, lease.LeaseId, handshake.DatabaseId, command.CommandId, body)
	if err != nil || status.Status != contract.CommandStatusStatusCompleted || status.TestId == nil {
		t.Fatalf("exact lookup: %+v %v", status, err)
	}
	testID := *status.TestId
	before, err := client.Audit(ctx, lease.LeaseId, testID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	equivalent := strings.Replace(string(body), `"areaM2":1`, `"areaM2":1.0`, 1)
	status, err = client.LookupCreateExact(ctx, lease.LeaseId, handshake.DatabaseId, command.CommandId, []byte(equivalent))
	if err != nil || status.TestId == nil || *status.TestId != testID {
		t.Fatalf("equivalent lookup: %+v %v", status, err)
	}
	different := strings.Replace(string(body), `"areaM2":1`, `"areaM2":1.0000000000000001`, 1)
	_, err = client.LookupCreateExact(ctx, lease.LeaseId, handshake.DatabaseId, command.CommandId, []byte(different))
	var failure *RequestError
	if !errors.As(err, &failure) || failure.Status != http.StatusConflict || failure.Code != "command_conflict" {
		t.Fatalf("exact conflict: %v", err)
	}
	_, err = client.LookupCreateExact(ctx, lease.LeaseId, uuid.New(), command.CommandId, body)
	if !errors.As(err, &failure) || failure.Status != http.StatusConflict || failure.Code != "database_mismatch" {
		t.Fatalf("database mismatch: %v", err)
	}
	missing := command
	missing.CommandId = uuid.New()
	missingBody, err := json.Marshal(missing)
	if err != nil {
		t.Fatal(err)
	}
	status, err = client.LookupCreateExact(ctx, lease.LeaseId, handshake.DatabaseId, missing.CommandId, missingBody)
	if err != nil || status.Status != contract.CommandStatusStatusUnknown {
		t.Fatalf("missing lookup: %+v %v", status, err)
	}
	after, err := client.Audit(ctx, lease.LeaseId, testID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) || drop.posts.Load() != 1 {
		t.Fatalf("lookup changed audit or repeated create: %v posts=%d", err, drop.posts.Load())
	}
	if err := process.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, next, err := Start(ctx, executable, profile, "test", "0.1.0")
	if reopened != nil {
		t.Cleanup(func() { _ = reopened.Close(ctx) })
	}
	if err != nil {
		t.Fatal(err)
	}
	if next.DatabaseId != handshake.DatabaseId || next.BackendRunId == handshake.BackendRunId {
		t.Fatalf("reopened identity: %+v", next)
	}
	client = reopened.Client()
	client.http.Transport = &droppedCreateResponse{next: client.http.Transport, posts: &posts}
	lease, err = client.AcquireLease(ctx, uuid.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	status, err = client.LookupCreateExact(ctx, lease.LeaseId, handshake.DatabaseId, command.CommandId, body)
	if err != nil || status.Status != contract.CommandStatusStatusCompleted || status.TestId == nil || *status.TestId != testID {
		t.Fatalf("reopened lookup: %+v %v", status, err)
	}
	_, err = client.LookupCreateExact(ctx, lease.LeaseId, handshake.DatabaseId, command.CommandId, []byte(different))
	if !errors.As(err, &failure) || failure.Status != http.StatusConflict || failure.Code != "command_conflict" {
		t.Fatalf("reopened conflict: %v", err)
	}
	list, err := client.ListTests(ctx, lease.LeaseId, contract.ListTestsParams{})
	if err != nil || len(list.Items) != 1 || list.Items[0].TestId != testID {
		t.Fatalf("reopened history: %+v %v", list, err)
	}
	reopenedAudit, err := client.Audit(ctx, lease.LeaseId, testID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var creates int
	for _, event := range reopenedAudit.Items {
		if event.Action == "create" && event.CommandId != nil && *event.CommandId == command.CommandId {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("create audit count: %d", creates)
	}
	if posts.Load() != 1 {
		t.Fatalf("create POST count after reopen: %d", posts.Load())
	}
	if err := reopened.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
