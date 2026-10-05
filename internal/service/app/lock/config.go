package lock

import (
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corelock "github.com/kitsunium/sdk/internal/core/app/lock"
)

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
