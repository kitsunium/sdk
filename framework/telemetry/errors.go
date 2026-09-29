// The sentinels of framework/telemetry. Each var's name equals its errs.Define
// Reason in SCREAMING_SNAKE form.

package telemetry

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// Misconfigured refuses an exporter that cannot be built as asked; the
	// rule field says which rule failed.
	Misconfigured = errs.Define(CodeMisconfigured, "MISCONFIGURED",
		"The telemetry exporter is misconfigured",
		"framework/telemetry: no socket path, a buffer outside [64, 1<<20], or a node ID outside the grammar; the rule field says which",
		errs.WithExitCode(78))

	// Running refuses a second Start.
	Running = errs.Define(CodeRunning, "RUNNING",
		"The telemetry exporter is already running",
		"framework/telemetry: Start called on a started exporter")
)
