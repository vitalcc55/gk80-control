package main

import (
	"context"
	"fmt"
	"os"

	"github.com/vitalcc55/gk80-control/internal/core"
)

func main() {
	if err := core.Run(context.Background(), os.Stdin, os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "GK80 core could not start or stopped unexpectedly")
		os.Exit(1)
	}
}
