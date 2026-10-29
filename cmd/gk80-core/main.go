package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/vitalcc55/gk80-control/internal/core"
	"github.com/vitalcc55/gk80-control/internal/diagnostics"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

func main() {
	if err := core.Run(context.Background(), os.Stdin, os.Stdout, os.Stderr); err != nil {
		if err == diagnostics.ErrCloseUnconfirmed {
			os.Exit(transport.CoreExitDiagnostics)
		}
		if errors.Is(err, diagnostics.ErrCloseUnconfirmed) {
			os.Exit(transport.CoreExitFailureAndDiagnostics)
		}
		_, _ = fmt.Fprintln(os.Stderr, "GK80 core could not start or stopped unexpectedly")
		os.Exit(1)
	}
}
