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
		"service/writer/journald: connecting the unix-datagram socket failed",
		errs.WithExitCode(exitIOErr))

	// JournaldWriteFailed wraps a datagram send failure on the journald socket.
	JournaldWriteFailed = errs.Define(CodeJournaldWriteFailed, "JOURNALD_WRITE_FAILED",
		"Journald sink write failed",
		"service/writer/journald: the unix-datagram socket returned an error",
		errs.WithExitCode(exitIOErr))
)

// wrapOpen wraps cause under the open sentinel. The socket path is NOT attached
// (it may be operator-sensitive); only a fixed private string is added.
func wrapOpen(cause error) error {
	//: single wrap point so every connect failure carries the open code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeJournaldOpenFailed,
		Reason:  "JOURNALD_OPEN_FAILED",
		Public:  "Journald sink could not connect the journal socket",
		Private: "service/writer/journald: connecting the unix-datagram socket failed",
	})
}

// wrapWrite wraps cause under the write sentinel with only the byte count.
func wrapWrite(cause error, n int) error {
	//: single wrap point so every send failure carries the write code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeJournaldWriteFailed,
		Reason:  "JOURNALD_WRITE_FAILED",
		Public:  "Journald sink write failed",
		Private: "service/writer/journald: the unix-datagram socket returned an error",
	}, errs.Int("bytes", n))
}
