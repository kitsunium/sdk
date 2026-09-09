// Package cache provides the concrete cache stores implementing
// internal/core/cache: a tagged, stampede-protected memory store over the
// kernel LRU+TTL primitive, and the chain that puts one store in front of
// another. Stdlib-only, cross-OS. ADR 0049.
package cache

import (
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/cache"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// MemoryConfig parameterises [NewMemory].
//
// Its zero value is deliberately NOT a working cache: MaxEntries must be
// stated. See [NewMemory] for why that refusal, and not a default, is the
// right ADR 0031 half here.
type MemoryConfig struct {
	// MaxEntries caps the live entry count; the least-recently-used entry is
	// evicted above it. It MUST be positive.
	MaxEntries int
	// DefaultTTL applies to every entry whose own TTL is zero. Zero means the
	// default is "no deadline", so entries leave only by eviction, by Delete,
	// or by tag invalidation. Negative is refused.
	DefaultTTL time.Duration
	// Clock is the time source, injectable so TTL is testable. nil means
	// clock.System. Only the reading half of the port is used — a cache reads
	// time, it never waits for it (ADR 0039).
	Clock clock.Clock
}

// validate applies ADR 0031 to the configuration: refuse what the SDK cannot
// choose on the caller's behalf, and reinterpret nothing.
func (c MemoryConfig) validate() error {
	//: a capacity IS the caller's intent — see NewMemory for why refusing beats
	//: any default the SDK could invent.
	if c.MaxEntries <= 0 {
		//: refuse loudly rather than build an unbounded cache.
		return kerrs.Wrap(corecache.CacheMisconfigured, kerrs.WrapParams{},
			kerrs.String("option", "MaxEntries"),
			kerrs.Int("value", c.MaxEntries))
	}
	//: a negative default TTL is a mistake, not a third meaning: the kernel
	//: primitive would read it as "no expiry", so accepting it produces a cache
	//: whose entries never leave — the exact opposite of what was written.
	if c.DefaultTTL < 0 {
		//: refuse rather than reinterpret.
		return kerrs.Wrap(corecache.CacheMisconfigured, kerrs.WrapParams{},
			kerrs.String("option", "DefaultTTL"),
			kerrs.String("value", c.DefaultTTL.String()))
	}
	//: usable as given.
	return nil
}

// clockOrSystem resolves the configured time source.
func (c MemoryConfig) clockOrSystem() clock.Clock {
	//: a nil clock is the production default, not a misconfiguration.
	if c.Clock == nil {
		//: the system time source.
		return clock.System
	}
	//: the injected source, as given.
	return c.Clock
}
