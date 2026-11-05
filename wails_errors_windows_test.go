//go:build desktopapp

package main

import (
	"errors"
	"testing"

	"github.com/vitalcc55/gk80-control/internal/diagnostics"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type wrappedWailsFailure struct{ cause error }

func (wrappedWailsFailure) Error() string   { panic("secret-marker must not be inspected") }
func (e wrappedWailsFailure) Unwrap() error { return e.cause }

func TestClassifyWailsErrorUsesTypeWithoutInspectingText(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		fatal bool
	}{
		{"nil", nil, false},
		{"unknown with misleading text", errors.New("fatal_error application_run_failed secret-marker"), false},
		{"unknown opaque wrapper", wrappedWailsFailure{errors.New("secret-marker")}, false},
		{"fatal", &application.FatalError{}, true},
		{"wrapped fatal", wrappedWailsFailure{&application.FatalError{}}, true},
		{"joined fatal", errors.Join(errors.New("secret-marker"), &application.FatalError{}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, fatal := classifyWailsError(tc.err)
			want := diagnostics.FrameworkUnclassified
			if tc.fatal {
				want = diagnostics.FrameworkFatalError
			}
			if code != want || fatal != tc.fatal {
				t.Fatalf("wrong code or changed fatal predicate: %s %t", code, fatal)
			}
		})
	}
}
