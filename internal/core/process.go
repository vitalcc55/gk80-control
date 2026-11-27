package core

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/vitalcc55/gk80-control/internal/diagnostics"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

// RunProcess implements the Core entry point for both executable launchers.
func RunProcess(input io.Reader, output, diagnosticsOutput io.Writer) int {
	err := Run(context.Background(), input, output, diagnosticsOutput)
	switch {
	case err == nil:
		return 0
	case err == diagnostics.ErrCloseUnconfirmed:
		return transport.CoreExitDiagnostics
	case errors.Is(err, diagnostics.ErrCloseUnconfirmed):
		return transport.CoreExitFailureAndDiagnostics
	default:
		_, _ = fmt.Fprintln(diagnosticsOutput, "GK80 core could not start or stopped unexpectedly")
		return 1
	}
}
