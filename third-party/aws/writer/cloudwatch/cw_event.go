// Package cloudwatch — the buffered log-event value type, in its own file per
// the one-exported-struct-per-file convention.
package cloudwatch

import "time"

// cwEvent is one buffered log event: its source timestamp + formatted message.
type cwEvent struct {
	// ts is the record's event time (falls back to now when zero).
	ts time.Time
	// msg is the encoded log line.
	msg string
}
