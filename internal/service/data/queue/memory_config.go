package queue

import (
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// receiptRadix is the base a memory receipt's counter is written in. Base 36
// keeps it short without needing a second alphabet, and nothing ever parses
// it back — a receipt is opaque.
const receiptRadix int = 36

// clockOrSystem resolves a nil clock to the wall clock.
func clockOrSystem(clk clock.Clock) clock.Clock {
	//: nil is the caller who has no opinion, which is the common case.
	if clk == nil {
		//: the production value.
		return clock.System
	}
	//: the caller's, usually a ManualClock in a test.
	return clk
}
