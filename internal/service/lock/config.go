// Package lock provides the concrete lockers implementing internal/core/lock:
// an in-process locker whose leases really expire, and a file locker whose
// leases really do not. Stdlib-only. ADR 0052.
package lock

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corelock "github.com/kitsunium/sdk/internal/core/lock"
)

// MemoryConfig parameterises [NewMemory].
//
// Its zero value is deliberately NOT a working locker: TTL must be stated.
// See [NewMemory] for why refusing, rather than defaulting, is the only
// defensible ADR 0031 half for a lease lifetime.
type MemoryConfig struct {
	// TTL is how long a lease is held before it can be taken by another
	// caller. It MUST be positive; zero and negative are refused at
	// construction, never reinterpreted.
	TTL time.Duration
	// Clock is the time source, injectable so expiry and waiting are testable
	// without sleeping. nil means clock.System.
	//
	// The whole port is needed, not just the reading half: this locker WAITS
	// for a holder's deadline rather than polling for it, so it needs
	// clock.Waiter as well as clock.Clock — which is what clock.Timed is
	// (ADR 0039).
	Clock clock.Timed
}

// validate applies ADR 0031: refuse what the SDK cannot choose on the caller's
// behalf, and reinterpret nothing.
func (c MemoryConfig) validate() error {
	//: the refusal this whole domain is shaped around. A zero TTL has two
	//: opposite natural readings — "already expired" and "never expires" — so
	//: any value chosen here is wrong for half its callers, and wrong in
	//: silence: a locker whose leases are born expired grants every request
	//: and excludes nobody.
	if c.TTL <= 0 {
		//: refuse at construction, where the program is wired.
		return kerrs.Wrap(corelock.LockMisconfigured, kerrs.WrapParams{},
			kerrs.String("option", "TTL"),
			kerrs.String("value", c.TTL.String()))
	}
	//: usable as given.
	return nil
}

// clockOrSystem resolves the configured time source.
func (c MemoryConfig) clockOrSystem() clock.Timed {
	//: a nil clock is the production default, not a misconfiguration.
	if c.Clock == nil {
		//: the system time source.
		return clock.System
	}
	//: the injected source, as given.
	return c.Clock
}
