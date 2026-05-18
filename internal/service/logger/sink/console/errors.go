// Package console — declares the sentinels returned by this
// package's constructor and Write method. Each var's name equals its
// errs.Define Reason in SCREAMING_SNAKE form.
package console

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitIOErr matches sysexits EX_IOERR — used by WriteFailed to let CLI
// consumers treat a console-write failure as an I/O problem rather than a
// generic internal software error (70).
const exitIOErr int = 74

var (
	// WriterNil is returned when New receives a nil io.Writer.
	WriterNil = errs.Define(CodeWriterNil, "WRITER_NIL",
		"Console sink requires a non-nil writer",
		"service/logger/sink/console.New called with nil io.Writer")

	// CtxCancelled wraps a cancelled context at Write time.
	CtxCancelled = errs.Define(CodeCtxCancelled, "CTX_CANCELLED",
		"Logging aborted due to cancellation",
		"service/logger/sink/console.Write invoked with cancelled context")

	// WriteFailed wraps the underlying writer's error at Write time.
	WriteFailed = errs.Define(CodeWriteFailed, "WRITE_FAILED",
		"Console write failed",
		"service/logger/sink/console.Write underlying io.Writer returned an error",
		errs.WithExitCode(exitIOErr))
)
