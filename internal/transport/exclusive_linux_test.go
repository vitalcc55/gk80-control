package transport

import (
	"net"
	"testing"
)

func verifyExclusiveListener(t *testing.T, address string) {
	t.Helper()
	listener, err := net.Listen("tcp4", address)
	if err == nil {
		_ = listener.Close()
		t.Fatal("another listener acquired the core address")
	}
}
