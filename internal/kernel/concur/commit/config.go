package commit

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// Config tunes a Group. The zero Config is usable: no item cap, no gathering
// wait — a batch is flushed as soon as the flush before it has finished, so
// the callers that arrived during one flush share the next.
type Config struct {
	// MaxItems seals a batch once it holds this many items: a full batch is
	// flushed without waiting out MaxWait, and the next caller starts another.
	// Zero or negative means no cap.
	MaxItems int
	// MaxWait is how long a batch gathers followers after its first item before
	// it is flushed. Zero means no wait: the batch is flushed as soon as the
	// one before it has been — coalescing then comes from the flushes' own
	// duration. Negative is read as zero.
	MaxWait time.Duration
	// Clock is what MaxWait is waited on. nil means clock.System; a
	// clock.ManualClock makes the gathering deterministic in a test.
	Clock clock.Waiter
}
