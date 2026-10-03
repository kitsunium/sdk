// Package journald — declares the sentinels returned by the journald sink's
// constructor and Write path. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form. No record content or socket path is ever echoed into
// these errors.
package journald

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitIOErr matches sysexits EX_IOERR — a journald transport failure is an I/O
// problem, not a generic internal software error (70).
const exitIOErr int = 74

var (
	// JournaldOpenFailed wraps a failure to connect the unix-datagram socket.
	JournaldOpenFailed = errs.Define(CodeJournaldOpenFailed, "JOURNALD_OPEN_FAILED",
		"Journald sink could not connect the journal socket",
		"service/observe/logger/writer/journald: connecting the unix-datagram socket failed",
		errs.WithExitCode(exitIOErr))

	// JournaldWriteFailed wraps a datagram send failure on the journald socket.
	JournaldWriteFailed = errs.Define(CodeJournaldWriteFailed, "JOURNALD_WRITE_FAILED",
		"Journald sink write failed",
		"service/observe/logger/writer/journald: the unix-datagram socket returned an error",
		errs.WithExitCode(exitIOErr))
)
