package stream

import (
	"github.com/kitsunium/sdk/internal/kernel/clock"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// MemoryConfig configures [NewMemory]: the policy it enforces and the clock
// that stamps every batch and times every wait.
type MemoryConfig struct {
	// Clock stamps [corestream.EntryValue].AppendedAt, ages records for
	// [corestream.PolicyValue].RetainFor, and times the wait
	// [corestream.PolicyValue].BlockTimeout allows, so a test drives
	// retention and backpressure with a ManualClock and no sleeping. Nil
	// means clock.System.
	Clock clock.Timed
	// Policy is what the stream enforces. Its zero value works: one
	// partition, records of up to corestream.DefaultMaxRecordBytes, kept
	// forever, never full.
	Policy corestream.PolicyValue
}

// clockOrSystem resolves a nil clock to the wall clock.
func clockOrSystem(clk clock.Timed) clock.Timed {
	//: nil is the caller who has no opinion, which is the common case.
	if clk == nil {
		//: the production value.
		return clock.System
	}
	//: the caller's, usually a ManualClock in a test.
	return clk
}
