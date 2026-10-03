// Package cache — the New constructor configuration.
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
}
