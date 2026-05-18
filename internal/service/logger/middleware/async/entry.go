// Package async — holds the recycled payload struct that travels
// through the ring buffer between the producer (Write) and the consumer
// (drain goroutine).
package async

import corelogger "github.com/kitsunium/sdk/internal/core/logger"

// recordEntry holds a single recycled Write payload. Producers populate
// the rec + data fields, push the entry through the ring, and the drainer
// recycles the entry once the downstream sink accepts it.
type recordEntry struct {
	// rec carries the originating record for the downstream sink.
	rec corelogger.RecordEvent
	// data carries the encoded bytes; the entry owns the slice for the
	// duration of its trip through the ring.
	data []byte
}

// newRecordEntry returns a fresh *recordEntry with a nil bytes slice; the
// producer owns sizing the slice when it copies the payload in.
func newRecordEntry() *recordEntry {
	//: zero-state entry; payload is set by the producer at Write time.
	return &recordEntry{}
}
