package transport

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

func TestCertificateMismatchDoesNotSendBearer(t *testing.T) {
	var received atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := NewClient(server.Listener.Addr().String(), sha256.Sum256([]byte("wrong certificate")), "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Handshake(context.Background()); err == nil {
		t.Fatal("untrusted server accepted")
	}
	if received.Load() {
		t.Fatal("request reached an untrusted server")
	}
}

func TestHandshakeRejectsInvalidResponseAndDoesNotFollowRedirect(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	databaseID := uuid.NewString()
	for _, payload := range []string{
		`{"apiVersion":"v1","backendRunId":"` + uuid.NewString() + `","databaseId":"` + databaseID + `"}`,
		`{"apiVersion":"v1","backendRunId":"` + uuid.NewString() + `","databaseId":"` + databaseID + `","demo":false}`,
		`{"apiVersion":"v2","backendRunId":"` + uuid.NewString() + `","databaseId":"` + databaseID + `","demo":true}`,
		`{"apiVersion":"v1","backendRunId":"invalid","databaseId":"` + databaseID + `","demo":true}`,
		strings.Repeat(" ", 8193),
		"redirect",
	} {
		t.Run(fmt.Sprintf("case-%d", len(payload)), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if payload == "redirect" {
					http.Redirect(w, r, target.URL, http.StatusFound)
					return
				}
				_, _ = io.WriteString(w, payload)
			}))
			defer server.Close()
			fingerprint := sha256.Sum256(server.Certificate().Raw)
			client, err := NewClient(server.Listener.Addr().String(), fingerprint, "test-secret")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Handshake(context.Background()); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
	if redirected.Load() {
		t.Fatal("redirected server received a request")
	}
}

func TestHandshakeRequiresDatabaseIdentity(t *testing.T) {
	runID, databaseID := uuid.NewString(), uuid.NewString()
	for _, tc := range []struct {
		name    string
		field   string
		wantErr bool
	}{
		{name: "valid", field: `,"databaseId":"` + databaseID + `"`},
		{name: "missing", wantErr: true},
		{name: "null", field: `,"databaseId":null`, wantErr: true},
		{name: "nil", field: `,"databaseId":"00000000-0000-0000-0000-000000000000"`, wantErr: true},
		{name: "invalid", field: `,"databaseId":"bad"`, wantErr: true},
		{name: "duplicate", field: `,"databaseId":"` + databaseID + `","databaseId":"` + databaseID + `"`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"apiVersion":"v1","backendRunId":"%s","demo":true%s}`, runID, tc.field)
			}))
			defer server.Close()
			client, err := NewClient(server.Listener.Addr().String(), sha256.Sum256(server.Certificate().Raw), "test-secret")
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_, err = client.Handshake(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("handshake error %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

func TestClientRejectsNonLoopbackAddress(t *testing.T) {
	if _, err := NewClient("127.0.0.2:1234", [32]byte{}, "secret"); err == nil {
		t.Fatal("non-canonical loopback accepted")
	}
}

func TestProcessRejectsChangedRunIdentityOnLaterHandshake(t *testing.T) {
	expected := uuid.New()
	databaseID := uuid.New()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"apiVersion":"v1","backendRunId":"%s","databaseId":"%s","demo":true}`, uuid.NewString(), databaseID)
	}))
	defer server.Close()
	client, err := NewClient(server.Listener.Addr().String(), sha256.Sum256(server.Certificate().Raw), "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	process := &Process{client: client, ready: Ready{BackendRunID: expected, APIVersion: "v1", Demo: true}, databaseID: databaseID}
	if _, err := process.Handshake(context.Background()); err == nil {
		t.Fatal("changed backend run accepted")
	}
}

func TestProcessRejectsChangedDatabaseIdentityOnLaterHandshake(t *testing.T) {
	runID, databaseID := uuid.New(), uuid.New()
	var current atomic.Pointer[uuid.UUID]
	current.Store(&databaseID)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"apiVersion":"v1","backendRunId":"%s","databaseId":"%s","demo":true}`, runID, *current.Load())
	}))
	defer server.Close()
	client, err := NewClient(server.Listener.Addr().String(), sha256.Sum256(server.Certificate().Raw), "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	process := &Process{client: client, ready: Ready{BackendRunID: runID, APIVersion: "v1", Demo: true}, databaseID: databaseID}
	if _, err := process.Handshake(context.Background()); err != nil {
		t.Fatalf("original identity rejected: %v", err)
	}
	changed := uuid.New()
	current.Store(&changed)
	if _, err := process.Handshake(context.Background()); err == nil {
		t.Fatal("changed database identity accepted")
	}
	current.Store(&databaseID)
	if _, err := process.Handshake(context.Background()); err != nil {
		t.Fatalf("original identity not restored: %v", err)
	}
}
