// Package id — declares the sentinel *errs.Error values for the generators.
package id

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// EntropyFailed wraps a crypto/rand.Read failure while drawing id bytes.
	EntropyFailed = errs.Define(CodeIDEntropyFailed, "ID_ENTROPY_FAILED",
		"Identifier generation failed to read secure random bytes",
		"service/id: crypto/rand.Read returned an error")

	// ClockBackwards is returned when the snowflake clock regressed past the
	// recoverable sequence window.
	ClockBackwards = errs.Define(CodeIDClockBackwards, "ID_CLOCK_BACKWARDS",
		"Identifier generation observed the clock moving backwards",
		"service/id: snowflake clock regressed beyond the same-millisecond sequence space")
)
