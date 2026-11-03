package cache

import (
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kcache "github.com/kitsunium/sdk/internal/kernel/collections/cache"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxStripes caps MemoryConfig.Stripes: past it, the stripes cost more memory
// and walk time (InvalidateTag, Expire) than any core count can win back.
const maxStripes int = 1024

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
	// clock.System. Only the reading half of the port is required — a cache
	// reads time (ADR 0039); LoadLeased alone waits, on Clock when it can wait
	// (clock.System, a clock.ManualClock) and on clock.System otherwise. A
	// clock.CoarseClock makes a TTL'd hit's clock read an atomic load, and
	// every expiry late by up to its resolution.
	Clock clock.Clock
	// Stripes splits the store into independent stripes, each with its own
	// lock, its own share of MaxEntries and MaxBytes and its own tag index, so
	// goroutines working on keys of different stripes never wait for one
	// another. 0 means 1, one store as before; above MaxEntries (or a positive
	// MaxBytes) it is held there, and above 1024 at 1024. Negative is refused.
	Stripes int
	// MaxBytes bounds the summed weight of the live values, as measured by the
	// weigher handed to [NewWeighedMemory]; entries are evicted above it as
	// above MaxEntries, both bounds applying. 0 means no byte bound. An entry
	// heavier than one stripe's share of it is refused with
	// CACHE_ENTRY_REJECTED at Set. Negative is refused, and so is a positive
	// MaxBytes given to [NewMemory], which has no weigher to measure it with.
	MaxBytes int64
	// Policy is the eviction policy: least recently used (the zero), or
	// S3-FIFO, which keeps a hot set through a scan.
	Policy kcache.Policy
	// LeasePoll is how often LoadLeased reads the store again while another
	// process holds a key's lease. 0 means 25ms, the file locker's own poll;
	// negative is refused.
	LeasePoll time.Duration
}

// validate applies ADR 0031 to the configuration: refuse what the SDK cannot
// choose on the caller's behalf, and reinterpret nothing.
func (c MemoryConfig) validate() error {
	//: a capacity IS the caller's intent — see NewMemory for why refusing beats
	//: any default the SDK could invent.
	if c.MaxEntries <= 0 {
		//: refuse loudly rather than build an unbounded cache.
		return misconfigured("MaxEntries", kerrs.Int("value", c.MaxEntries))
	}
	//: a negative default TTL is a mistake, not a third meaning: the kernel
	//: primitive would read it as "no expiry", so accepting it produces a cache
	//: whose entries never leave — the exact opposite of what was written.
	if c.DefaultTTL < 0 {
		//: refuse rather than reinterpret.
		return misconfigured("DefaultTTL", kerrs.String("value", c.DefaultTTL.String()))
	}
	//: a negative stripe count means nothing.
	if c.Stripes < 0 {
		//: refuse rather than reinterpret.
		return misconfigured("Stripes", kerrs.Int("value", c.Stripes))
	}
	//: a negative byte bound means nothing.
	if c.MaxBytes < 0 {
		//: refuse rather than reinterpret.
		return misconfigured("MaxBytes", kerrs.Int64("value", c.MaxBytes))
	}
	//: a negative poll step means nothing.
	if c.LeasePoll < 0 {
		//: refuse rather than reinterpret.
		return misconfigured("LeasePoll", kerrs.String("value", c.LeasePoll.String()))
	}
	//: usable as given.
	return nil
}

// misconfigured builds the CACHE_MISCONFIGURED refusal naming option.
func misconfigured(option string, fields ...kerrs.FieldValue) error {
	//: origin-wins keeps the sentinel's code; the fields say which option.
	return kerrs.Wrap(corecache.CacheMisconfigured, kerrs.WrapParams{},
		append([]kerrs.FieldValue{kerrs.String("option", option)}, fields...)...)
}

// stripeCount resolves Stripes: 0 is 1, and the count never exceeds the
// entries nor the bytes — every stripe holds at least one of each bound, a
// zero share reading as no bound — nor maxStripes.
func (c MemoryConfig) stripeCount() int {
	count := min(max(c.Stripes, 1), c.MaxEntries, maxStripes)
	//: a byte bound smaller than the count would leave a stripe a zero share.
	if c.MaxBytes > 0 {
		count = int(min(int64(count), c.MaxBytes))
	}
	//: at least one, at most one per entry, per byte and maxStripes.
	return count
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

// shareOf returns stripe at's share of bound over count stripes, the
// remainder to the first stripes; 0 stays 0 (no bound).
func shareOf(bound, count, at int) int {
	share := bound / count
	//: the first bound%count stripes take one more each.
	if at < bound%count {
		share++
	}
	//: this stripe's share.
	return share
}

// shareOf64 is shareOf for a byte bound.
func shareOf64(bound int64, count, at int) int64 {
	share := bound / int64(count)
	//: the first bound%count stripes take one more each.
	if int64(at) < bound%int64(count) {
		share++
	}
	//: this stripe's share.
	return share
}
