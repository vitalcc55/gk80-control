// Package api exposes the canonical contract embedded in native binaries.
package api

import _ "embed"

//go:embed core.yaml
var coreSchema string

func CoreSchema() string { return coreSchema }
