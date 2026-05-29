// Package logger — declares pkg/v1/logger's sentinels. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
package logger

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// WriterRequired is returned when NewText is called with Config.Writer==nil.
	// Callers that want the "stderr convenience" use Default() instead.
	WriterRequired = errs.Define(CodeWriterRequired, "WRITER_REQUIRED",
		"Logger config requires an explicit writer",
		"pkg/v1/logger.NewText called with Config.Writer==nil; supply an io.Writer or use logger.Default()")

	// SinkConfigRequired is returned when NewWithSink is called with
	// SinkConfig.Sink==nil. Callers that want a console sink use
	// logger.ConsoleStderr / ConsoleStdout or stick to logger.Default()
	// for the legacy text-on-stderr wiring.
	SinkConfigRequired = errs.Define(CodeSinkConfigRequired, "SINK_CONFIG_REQUIRED",
		"Logger SinkConfig requires an explicit sink",
		"pkg/v1/logger.NewWithSink called with SinkConfig.Sink==nil; supply a Sink or use logger.Default()")

	// WriterSpecInvalid is returned when NewMulti is called with zero
	// WriterSpec entries. Supply at least one named writer (and blank-import
	// its package) so the fan-out has a destination.
	WriterSpecInvalid = errs.Define(CodeWriterSpecInvalid, "WRITER_SPEC_INVALID",
		"NewMulti requires at least one writer spec",
		"pkg/v1/logger.NewMulti called with no WriterSpec entries; supply at least one named writer")
)
