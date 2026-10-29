package transport

import (
	"context"
	"net"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func verifyExclusiveListener(t *testing.T, address string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestExclusiveListenerHelper$")
	cmd.Env = append(os.Environ(), "GK80_TEST_ADDRESS="+address)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("socket was not exclusive: %v %s", err, output)
	}
}

func TestExclusiveListenerHelper(t *testing.T) {
	address := os.Getenv("GK80_TEST_ADDRESS")
	if address == "" {
		return
	}
	optionSet := false
	config := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
		var optionErr error
		if err := raw.Control(func(fd uintptr) {
			optionErr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
			optionSet = optionErr == nil
		}); err != nil {
			return err
		}
		return optionErr
	}}
	listener, err := config.Listen(context.Background(), "tcp4", address)
	if err == nil {
		_ = listener.Close()
		os.Exit(1)
	}
	if !optionSet {
		t.Fatal("could not enable SO_REUSEADDR in helper")
	}
}
