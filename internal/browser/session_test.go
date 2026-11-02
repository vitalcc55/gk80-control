package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequestUsesCurrentConnectionAndRejectsForeignOrigin(t *testing.T) {
	s, generation := testSession(t, "127.0.0.1:19080")
	called := 0
	h := s.protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called++; w.WriteHeader(http.StatusNoContent) }))
	for _, tc := range []struct {
		origin, connection string
		want               int
	}{
		{"http://127.0.0.1:19080", generation, http.StatusNoContent},
		{"http://evil.invalid", generation, http.StatusForbidden},
		{"http://127.0.0.1:19080", "stale", http.StatusConflict},
		{"http://127.0.0.1:19080", "", http.StatusConflict},
	} {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:19080/action", nil)
		r.Header.Set("Origin", tc.origin)
		if tc.connection != "" {
			r.Header.Set("X-GK80-Channel", tc.connection)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("origin %q connection %q: got %d want %d", tc.origin, tc.connection, w.Code, tc.want)
		}
	}
	if called != 1 {
		t.Fatalf("handler called %d times", called)
	}
}

func TestRevocationCancelsAdmittedRequestAndRejectsLaterRequests(t *testing.T) {
	s, generation := testSession(t, "127.0.0.1:19080")
	entered := make(chan context.Context, 1)
	done := make(chan struct{})
	h := s.protect(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		entered <- r.Context()
		<-r.Context().Done()
		close(done)
	}))
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19080/state", nil)
	r.Header.Set("X-GK80-Channel", generation)
	go h.ServeHTTP(httptest.NewRecorder(), r)
	ctx := <-entered
	s.revoke()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("request not cancelled")
	}
	<-done
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("revoked session accepted: %d", w.Code)
	}
}

func TestAdmissionIsCancelledSynchronouslyByRevocation(t *testing.T) {
	s, generation := testSession(t, "127.0.0.1:19080")
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19080/state", nil)
	r.Header.Set("X-GK80-Channel", generation)
	ctx, release, status := s.admitClass(r, ordinaryRequest)
	if status != 0 {
		t.Fatal("session not admitted")
	}
	defer release()
	child, childCancel := context.WithCancel(ctx)
	defer childCancel()
	s.revoke()
	if ctx.Err() == nil {
		t.Fatal("admitted request remains live after revocation returned")
	}
	if child.Err() == nil {
		t.Fatal("downstream context remains live after revocation returned")
	}
}

func TestAdmissionPreservesRequestDeadlineAndValues(t *testing.T) {
	s, generation := testSession(t, "127.0.0.1:19080")
	type key struct{}
	deadline := time.Now().Add(10 * time.Second)
	original, cancel := context.WithDeadline(context.WithValue(context.Background(), key{}, "request-value"), deadline)
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19080/state", nil).WithContext(original)
	r.Header.Set("X-GK80-Channel", generation)
	ctx, release, status := s.admitClass(r, ordinaryRequest)
	if status != 0 {
		t.Fatal("session not admitted")
	}
	defer release()
	got, ok := ctx.Deadline()
	if !ok || !got.Equal(deadline) {
		t.Fatalf("request deadline lost: %v %v", got, ok)
	}
	if ctx.Value(key{}) != "request-value" {
		t.Fatal("request value lost")
	}
}

func TestAdmissionTracksClientCancellationAndBoundsRequests(t *testing.T) {
	s, generation := testSession(t, "127.0.0.1:19080")
	client, clientCancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19080/state", nil).WithContext(client)
	r.Header.Set("X-GK80-Channel", generation)
	ctx, release, status := s.admitClass(r, ordinaryRequest)
	if status != 0 {
		t.Fatal("session not admitted")
	}
	child, childCancel := context.WithCancel(ctx)
	clientCancel()
	if ctx.Err() == nil || child.Err() == nil {
		t.Fatal("client cancellation did not reach downstream context")
	}
	childCancel()
	release()
	r = r.WithContext(context.Background())
	var releases []func()
	for i := 0; i < 60; i++ {
		_, release, status := s.admitClass(r, ordinaryRequest)
		if status != 0 {
			t.Fatalf("request %d rejected: %d", i, status)
		}
		releases = append(releases, release)
	}
	if _, _, status := s.admitClass(r, ordinaryRequest); status != http.StatusTooManyRequests {
		t.Fatalf("limit not enforced: %d", status)
	}
	for _, release := range releases {
		release()
	}
}

func TestReservedAdmissionsRemainIndependent(t *testing.T) {
	s, generation := testSession(t, "127.0.0.1:19080")
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19080/state", nil)
	r.Header.Set("X-GK80-Channel", generation)
	var releases []func()
	var contexts []context.Context
	for i := 0; i < 60; i++ {
		ctx, release, status := s.admitClass(r, ordinaryRequest)
		if status != 0 {
			t.Fatalf("ordinary request %d rejected: %d", i, status)
		}
		releases = append(releases, release)
		contexts = append(contexts, ctx)
	}
	r.Header.Set("X-Priority", "stop")
	if _, _, status := s.admitClass(r, ordinaryRequest); status != http.StatusTooManyRequests {
		t.Fatalf("ordinary request bypassed limit: %d", status)
	}
	for _, class := range []requestClass{stopRequest, heartbeatRequest} {
		var firstRelease func()
		for i := 0; i < 2; i++ {
			ctx, release, status := s.admitClass(r, class)
			if status != 0 {
				t.Fatalf("reserved request %d rejected: %d", i, status)
			}
			releases = append(releases, release)
			contexts = append(contexts, ctx)
			if i == 0 {
				firstRelease = release
			}
		}
		if _, _, status := s.admitClass(r, class); status != http.StatusTooManyRequests {
			t.Fatalf("reserved limit not enforced: %d", status)
		}
		firstRelease()
		ctx, release, status := s.admitClass(r, class)
		if status != 0 {
			t.Fatalf("released reserved slot not reusable: %d", status)
		}
		contexts = append(contexts, ctx)
		releases = append(releases, release)
	}
	s.revoke()
	for _, ctx := range contexts {
		if ctx.Err() == nil {
			t.Fatal("revocation left an admitted request active")
		}
	}
	for _, release := range releases {
		release()
	}
	if _, _, status := s.admitClass(r, stopRequest); status != http.StatusConflict {
		t.Fatalf("revoked stop admitted: %d", status)
	}
}

func TestReleaseCancelsBeforeRemovingAdmission(t *testing.T) {
	s, generation := testSession(t, "127.0.0.1:19080")
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19080/state", nil)
	r.Header.Set("X-GK80-Channel", generation)
	ctx, release, status := s.admitClass(r, ordinaryRequest)
	if status != 0 {
		t.Fatal("session not admitted")
	}
	completed := make(chan struct{})
	s.mu.Lock()
	go func() { release(); close(completed) }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		s.mu.Unlock()
		t.Fatal("release waited for lock before cancelling request")
	}
	s.mu.Unlock()
	<-completed
}

func TestCancelledClientAndUnsafeHostAreRejected(t *testing.T) {
	for _, host := range []string{"localhost:19080", "0.0.0.0:19080", "127.0.0.1:0", "127.0.0.1:65536"} {
		if _, err := newSession(host); err == nil {
			t.Fatalf("accepted host %q", host)
		}
	}
	s, generation := testSession(t, "127.0.0.1:19080")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19080/state", nil).WithContext(ctx)
	r.Header.Set("X-GK80-Channel", generation)
	called := false
	w := httptest.NewRecorder()
	s.protect(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(w, r)
	if called || w.Code != http.StatusRequestTimeout {
		t.Fatalf("cancelled request admitted: %d", w.Code)
	}
}
