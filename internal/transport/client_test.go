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
	for _, payload := range []string{
		`{"apiVersion":"v1","backendRunId":"` + uuid.NewString() + `"}`,
		`{"apiVersion":"v1","backendRunId":"` + uuid.NewString() + `","demo":false}`,
		`{"apiVersion":"v2","backendRunId":"` + uuid.NewString() + `","demo":true}`,
		`{"apiVersion":"v1","backendRunId":"invalid","demo":true}`,
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

func TestClientRejectsNonLoopbackAddress(t *testing.T) {
	if _, err := NewClient("127.0.0.2:1234", [32]byte{}, "secret"); err == nil {
		t.Fatal("non-canonical loopback accepted")
	}
}

func TestProcessRejectsChangedRunIdentityOnLaterHandshake(t *testing.T) {
	expected := uuid.New()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"apiVersion":"v1","backendRunId":"%s","demo":true}`, uuid.NewString())
	}))
	defer server.Close()
	client, err := NewClient(server.Listener.Addr().String(), sha256.Sum256(server.Certificate().Raw), "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	process := &Process{client: client, ready: Ready{BackendRunID: expected, APIVersion: "v1", Demo: true}}
	if _, err := process.Handshake(context.Background()); err == nil {
		t.Fatal("changed backend run accepted")
	}
}
