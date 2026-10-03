// Package journald — the wrap points every failure of this writer goes through,
// so each carries its code from internal/core/observe/logger/writer/journald.
package journald

import (
	corejournald "github.com/kitsunium/sdk/internal/core/observe/logger/writer/journald"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// wrapOpen wraps cause under the open sentinel. The socket path is NOT attached
// (it may be operator-sensitive); only a fixed private string is added.
func wrapOpen(cause error) error {
	//: single wrap point so every connect failure carries the open code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    corejournald.CodeJournaldOpenFailed,
		Reason:  "JOURNALD_OPEN_FAILED",
		Public:  "Journald sink could not connect the journal socket",
		Private: "service/observe/logger/writer/journald: connecting the unix-datagram socket failed",
	})
}

// wrapWrite wraps cause under the write sentinel with only the byte count.
func wrapWrite(cause error, n int) error {
	//: single wrap point so every send failure carries the write code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    corejournald.CodeJournaldWriteFailed,
		Reason:  "JOURNALD_WRITE_FAILED",
		Public:  "Journald sink write failed",
		Private: "service/observe/logger/writer/journald: the unix-datagram socket returned an error",
	}, errs.Int("bytes", n))
}
