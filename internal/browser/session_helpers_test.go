package browser

import (
	"context"
	"testing"
)

// Unit-route tests exercise admission without starting another HTTP server.
func testSession(t *testing.T, host string) (*browserSession, string) {
	t.Helper()
	s, err := newSession(host)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	generation := s.instance + ":1"
	s.channel = &browserChannel{generation: generation, ctx: ctx, cancel: cancel, close: func() {}}
	return s, generation
}
