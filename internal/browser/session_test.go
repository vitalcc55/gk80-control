package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestSessionRequiresPairingAndRejectsForeignOrigin(t *testing.T) {
	s, code, err := newSession("127.0.0.1:19080", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.pair("wrong"); err == nil {
		t.Fatal("wrong code accepted")
	}
	cookie, proof, err := s.pair(code)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.pair(code); err == nil {
		t.Fatal("code reused")
	}
	called := false
	h := s.protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(http.StatusNoContent) }))
	request := func(origin string, withProof bool) int {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:19080/action", nil)
		r.Header.Set("Origin", origin)
		if withProof {
			r.AddCookie(cookie)
			r.Header.Set("X-GK80-Session", proof)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if request("http://127.0.0.1:19080", false) != http.StatusUnauthorized {
		t.Fatal("missing session accepted")
	}
	if request("http://evil.invalid", true) != http.StatusForbidden {
		t.Fatal("foreign origin accepted")
	}
	if called {
		t.Fatal("unauthorised request reached handler")
	}
	if request("http://127.0.0.1:19080", true) != http.StatusNoContent || !called {
		t.Fatal("authorised request rejected")
	}
}

func TestSessionRejectsMissingProofAndInvalidRequestOrigin(t *testing.T) {
	s, code, err := newSession("127.0.0.1:19080", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cookie, proof, err := s.pair(code)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, host, origin, token string
		withCookie                bool
		want                      int
	}{
		{"wrong host", "127.0.0.1:19081", "http://127.0.0.1:19080", proof, true, http.StatusForbidden},
		{"missing origin", "127.0.0.1:19080", "", proof, true, http.StatusForbidden},
		{"wrong proof", "127.0.0.1:19080", "http://127.0.0.1:19080", "wrong", true, http.StatusUnauthorized},
		{"missing proof", "127.0.0.1:19080", "http://127.0.0.1:19080", "", true, http.StatusUnauthorized},
		{"missing cookie", "127.0.0.1:19080", "http://127.0.0.1:19080", proof, false, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:19080/action", nil)
			r.Host = tc.host
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-GK80-Session", tc.token)
			if tc.withCookie {
				r.AddCookie(cookie)
			}
			w := httptest.NewRecorder()
			s.protect(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unauthorised request reached handler") })).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestConcurrentPairingAllowsOnlyOneSession(t *testing.T) {
	s, code, err := newSession("127.0.0.1:19080", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, err := s.pair(code); results <- err == nil }()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for ok := range results {
		if ok {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d sessions", accepted)
	}
}

func TestExpiryCancelsAndRejectsSession(t *testing.T) {
	s, code, err := newSession("127.0.0.1:19080", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s.validFor = 300 * time.Millisecond
	cookie, proof, err := s.pair(code)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19080/state", nil)
	r.AddCookie(cookie)
	r.Header.Set("X-GK80-Session", proof)
	ctx, release, status := s.admit(r)
	if status != 0 {
		t.Fatal("session not admitted before expiry")
	}
	defer release()
	child, childCancel := context.WithCancel(ctx)
	defer childCancel()
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("session did not expire")
		}
	}
	if ctx.Err() == nil || child.Err() == nil {
		t.Fatal("expiry did not cancel admitted request")
	}
	w := httptest.NewRecorder()
	s.protect(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("expired session reached handler") })).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired session accepted: %d", w.Code)
	}
}

func TestRevocationCancelsAdmittedRequestAndRejectsLaterRequests(t *testing.T) {
	s, code, err := newSession("127.0.0.1:19080", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cookie, proof, err := s.pair(code)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan context.Context, 1)
	done := make(chan struct{})
	h := s.protect(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		entered <- r.Context()
		<-r.Context().Done()
		close(done)
	}))
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19080/state", nil)
	r.AddCookie(cookie)
	r.Header.Set("X-GK80-Session", proof)
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
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session accepted: %d", w.Code)
	}
}

func TestAdmissionIsCancelledSynchronouslyByRevocation(t *testing.T) {
	s, code, err := newSession("127.0.0.1:19080", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cookie, proof, err := s.pair(code)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19080/state", nil)
	r.AddCookie(cookie)
	r.Header.Set("X-GK80-Session", proof)
	ctx, release, status := s.admit(r)
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
	s, code, err := newSession("127.0.0.1:19080", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cookie, proof, err := s.pair(code)
	if err != nil {
		t.Fatal(err)
	}
	type key struct{}
	deadline := time.Now().Add(10 * time.Second)
	original, cancel := context.WithDeadline(context.WithValue(context.Background(), key{}, "request-value"), deadline)
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19080/state", nil).WithContext(original)
	r.AddCookie(cookie)
	r.Header.Set("X-GK80-Session", proof)
	ctx, release, status := s.admit(r)
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
	s, code, err := newSession("127.0.0.1:19080", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cookie, proof, err := s.pair(code)
	if err != nil {
		t.Fatal(err)
	}
	client, clientCancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19080/state", nil).WithContext(client)
	r.AddCookie(cookie)
	r.Header.Set("X-GK80-Session", proof)
	ctx, release, status := s.admit(r)
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
	for i := 0; i < 64; i++ {
		_, release, status := s.admit(r)
		if status != 0 {
			t.Fatalf("request %d rejected: %d", i, status)
		}
		releases = append(releases, release)
	}
	if _, _, status := s.admit(r); status != http.StatusTooManyRequests {
		t.Fatalf("limit not enforced: %d", status)
	}
	for _, release := range releases {
		release()
	}
}

func TestReleaseCancelsBeforeRemovingAdmission(t *testing.T) {
	s, code, err := newSession("127.0.0.1:19080", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cookie, proof, err := s.pair(code)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19080/state", nil)
	r.AddCookie(cookie)
	r.Header.Set("X-GK80-Session", proof)
	ctx, release, status := s.admit(r)
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
		if _, _, err := newSession(host, time.Minute); err == nil {
			t.Fatalf("accepted host %q", host)
		}
	}
	s, code, err := newSession("127.0.0.1:19080", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cookie, proof, err := s.pair(code)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19080/state", nil).WithContext(ctx)
	r.AddCookie(cookie)
	r.Header.Set("X-GK80-Session", proof)
	called := false
	w := httptest.NewRecorder()
	s.protect(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(w, r)
	if called || w.Code != http.StatusRequestTimeout {
		t.Fatalf("cancelled request admitted: %d", w.Code)
	}
}

func TestPairingExpiresAndLimitsAttempts(t *testing.T) {
	s, code, err := newSession("127.0.0.1:19080", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		_, _, _ = s.pair("wrong")
	}
	if _, _, err := s.pair(code); err == nil {
		t.Fatal("code accepted after attempt limit")
	}
	s, code, err = newSession("127.0.0.1:19080", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s.codeUntil = time.Now().Add(-time.Second)
	if _, _, err := s.pair(code); err == nil {
		t.Fatal("expired code accepted")
	}
}
