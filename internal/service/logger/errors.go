// Package logger: errors.go declares the sentinels returned by this
// package's constructors and Handle methods. Each var's name equals its
// errs.Define Reason in SCREAMING_SNAKE form.
package logger

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitIOErr matches sysexits EX_IOERR — used by WriteFailed to let CLI
// consumers treat a log-write failure as an I/O problem rather than a
// generic internal software error (70).
const exitIOErr int = 74

var (
	// WriterNil is returned when NewTextHandler receives a nil io.Writer.
	WriterNil = errs.Define(CodeWriterNil, "WRITER_NIL",
		"Log handler requires a non-nil writer",
		"service/logger.NewTextHandler called with nil io.Writer")

	// HandlerNil is returned when New receives a nil corelogger.Handler.
	HandlerNil = errs.Define(CodeHandlerNil, "HANDLER_NIL",
		"Logger requires a non-nil handler",
		"service/logger.New called with nil Handler")

	// EncoderNil is returned when NewHandler receives a nil Encoder.
	EncoderNil = errs.Define(CodeEncoderNil, "ENCODER_NIL",
		"Logger handler requires a non-nil encoder",
		"service/logger.NewHandler called with nil Encoder")

	// SinkRequired is returned when NewHandler receives a nil Sink.
	SinkRequired = errs.Define(CodeSinkRequired, "SINK_REQUIRED",
		"Logger handler requires a non-nil sink",
		"service/logger.NewHandler called with nil Sink")

	// CtxCancelled wraps a cancelled context at Handle time.
	CtxCancelled = errs.Define(CodeCtxCancelled, "CTX_CANCELLED",
		"Logging aborted due to cancellation",
		"service/logger.TextHandler.Handle invoked with cancelled context")

	// WriteFailed wraps the underlying writer's error at Handle time.
	WriteFailed = errs.Define(CodeWriteFailed, "WRITE_FAILED",
		"Log write failed",
		"service/logger.TextHandler.Handle underlying writer returned an error",
		errs.WithExitCode(exitIOErr))
)
