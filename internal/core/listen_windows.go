package core

import (
	"context"
	"net"
	"syscall"

	"golang.org/x/sys/windows"
)

func listen(ctx context.Context) (net.Listener, error) {
	config := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
		var socketErr error
		err := raw.Control(func(fd uintptr) {
			// Winsock defines SO_EXCLUSIVEADDRUSE as the bitwise complement of SO_REUSEADDR.
			socketErr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, ^windows.SO_REUSEADDR, 1)
		})
		if err != nil {
			return err
		}
		return socketErr
	}}
	return config.Listen(ctx, "tcp4", "127.0.0.1:0")
}
