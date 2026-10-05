package cache

import (
	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

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
