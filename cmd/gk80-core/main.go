package main

import (
	"os"

	"github.com/vitalcc55/gk80-control/internal/core"
)

func main() {
	os.Exit(core.RunProcess(os.Stdin, os.Stdout, os.Stderr))
}
