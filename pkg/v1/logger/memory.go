// Package logger — exposes the in-memory test sink (NewMemorySink) and its
// RecordSnapshot element type so consumers can assert on what was logged.
package logger

import (
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/service/logger/sink/memory"
)

// RecordSnapshot is a buffered copy of a single recorded log event. It is the
// element type returned by MemorySink.Records, letting tests assert on a
// record's Level, Message, and Attrs without parsing an encoder's byte output.
// It is the same type as Record (an alias of the core RecordEvent).
type RecordSnapshot = corelogger.RecordEvent

// MemorySink is a Sink that buffers a defensive snapshot of every received
// record in a mutex-guarded slice. It is intended for tests that assert on
// what was logged. Pass it to NewWithSink (it satisfies Sink), retrieve the
// buffered records with Records, and clear them with Reset.
type MemorySink = memory.Memory

// NewMemorySink returns an empty MemorySink ready to record received records.
func NewMemorySink() *MemorySink {
	//: delegate to the in-memory sink implementation.
	return memory.NewMemory()
}
