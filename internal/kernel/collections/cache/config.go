package cache

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// Config parameterises a Cache. The zero value is valid: an unbounded cache with
// no default TTL, the system clock, and no eviction callback.
type Config[K comparable, V any] struct {
	// MaxEntries caps the live entry count (LRU eviction above it). 0 = unbounded.
	MaxEntries int
	// DefaultTTL applies to Set entries (SetTTL overrides per-entry). 0 = no expiry.
	DefaultTTL time.Duration
	// Clock is the time source (injectable for tests). nil = clock.System.
	Clock clock.Clock
	// OnEvict, if non-nil, is called with the key+value of every entry removed by
	// capacity eviction or expiry (not by an explicit Delete/Set-overwrite).
	OnEvict func(key K, val V)
	// MaxWeight caps the summed weight of the live entries, evicting above it as
	// MaxEntries does; both apply when both are set. 0 = unbounded. An entry
	// heavier than MaxWeight alone is stored and evicts every other entry: the
	// primitive has no error to refuse it with.
	MaxWeight int64
	// WeightOf, if non-nil, weighs an entry when it is stored; a negative answer
	// counts as 0, and one above math.MaxInt32 as math.MaxInt32. nil = every
	// entry weighs 1, so MaxWeight is then a count.
	WeightOf func(key K, val V) int64
	// Policy chooses the victim when the cache is full. The zero value is
	// PolicyLRU.
	Policy Policy
}
