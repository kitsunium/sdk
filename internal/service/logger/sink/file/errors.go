// Package file: errors.go declares the sentinels returned by this package's
// constructor and Write / Flush / Close methods. Each var's name equals its
// errs.Define Reason in SCREAMING_SNAKE form.
package file

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitIOErr matches sysexits EX_IOERR — used by WriteFailed and SyncFailed
// to let CLI consumers treat a file-write failure as an I/O problem rather
// than a generic internal software error (70).
const exitIOErr int = 74

var (
	// PathEmpty is returned when New receives an empty file path.
	PathEmpty = errs.Define(CodePathEmpty, "PATH_EMPTY",
		"File sink requires a non-empty path",
		"service/logger/sink/file.New called with empty path")

	// OpenFailed wraps an os.OpenFile failure at construction time.
	OpenFailed = errs.Define(CodeOpenFailed, "OPEN_FAILED",
		"File sink could not open the destination file",
		"service/logger/sink/file.New: os.OpenFile returned an error",
		errs.WithExitCode(exitIOErr))

	// CtxCancelled wraps a cancelled context at Write time.
	CtxCancelled = errs.Define(CodeCtxCancelled, "CTX_CANCELLED",
		"Logging aborted due to cancellation",
		"service/logger/sink/file.Write invoked with cancelled context")

	// WriteFailed wraps the underlying *os.File.Write error at Write time.
	WriteFailed = errs.Define(CodeWriteFailed, "WRITE_FAILED",
		"File write failed",
		"service/logger/sink/file.Write underlying *os.File returned an error",
		errs.WithExitCode(exitIOErr))

	// SyncFailed wraps the underlying *os.File.Sync error at Flush time.
	SyncFailed = errs.Define(CodeSyncFailed, "SYNC_FAILED",
		"File flush failed",
		"service/logger/sink/file.Flush underlying *os.File.Sync returned an error",
		errs.WithExitCode(exitIOErr))

	// CloseFailed wraps the underlying *os.File.Close error at Close time.
	CloseFailed = errs.Define(CodeCloseFailed, "CLOSE_FAILED",
		"File close failed",
		"service/logger/sink/file.Close underlying *os.File.Close returned an error",
		errs.WithExitCode(exitIOErr))
)
