package diagnostics

import "log/slog"

type FrameworkErrorCode string

const (
	FrameworkUnclassified         FrameworkErrorCode = "unclassified"
	FrameworkFatalError           FrameworkErrorCode = "fatal_error"
	FrameworkApplicationRunFailed FrameworkErrorCode = "application_run_failed"
)

// FrameworkOperation is a trusted reporting point, not the cause of the failure.
type FrameworkOperation string

const (
	FrameworkErrorHandler   FrameworkOperation = "error_handler"
	FrameworkApplicationRun FrameworkOperation = "application_run"
)

// FrameworkFailure binds only catalogued facts; slog attrs remain untrusted.
// Creating another logger shares the existing writer and registers no callbacks.
func (l *Logger) FrameworkFailure(code FrameworkErrorCode, operation FrameworkOperation) *slog.Logger {
	values := fields{errorCode: string(FrameworkUnclassified)}
	if (operation == FrameworkErrorHandler && (code == FrameworkFatalError || code == FrameworkUnclassified)) ||
		(operation == FrameworkApplicationRun && code == FrameworkApplicationRunFailed) {
		values.errorCode = string(code)
		values.operation = string(operation)
	}
	return slog.New(frameworkHandler{logger: l, event: frameworkMessage, component: ComponentWails, values: values})
}
