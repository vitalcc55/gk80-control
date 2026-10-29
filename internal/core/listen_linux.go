package core

import (
	"context"
	"net"
)

func listen(ctx context.Context) (net.Listener, error) {
	var config net.ListenConfig
	return config.Listen(ctx, "tcp4", "127.0.0.1:0")
}
