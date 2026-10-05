package session

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	coresession "github.com/kitsunium/sdk/internal/core/security/session"
)

// window is the validated deadline policy every store carries.
func (c Config) window() window {
	//: a nil clock is a working configuration, so it is filled, not refused.
	clk := c.Clock
	//: fall back to the wall clock.
	if clk == nil {
		//: the shared, concurrency-safe system reading.
		clk = clock.System
	}
	//: validation has already run by the time this is called.
	return window{idle: c.IdleTimeout, absolute: c.AbsoluteTimeout, clk: clk}
}

// validateWindow refuses a timeout pair that could never work. It is shared by
// Config and FileConfig so the two cannot drift into different rules.
func validateWindow(idle, absolute time.Duration) error {
	//: a non-positive idle window is not "expire now", it is a missing value.
	if idle <= 0 {
		//: name the field; never guess a duration on the caller's behalf.
		return wrapAs(coresession.InvalidConfig, nil, kerrs.String("field", "IdleTimeout"))
	}
	//: same for the ceiling.
	if absolute <= 0 {
		//: name the field.
		return wrapAs(coresession.InvalidConfig, nil, kerrs.String("field", "AbsoluteTimeout"))
	}
	//: an idle window that the ceiling makes unreachable is a sliding expiry
	//: the caller thinks they have and does not.
	if idle >= absolute {
		//: name both, so the fix is obvious without reading this file.
		return wrapAs(coresession.InvalidConfig, nil,
			kerrs.String("field", "IdleTimeout"),
			kerrs.String("constraint", "IdleTimeout < AbsoluteTimeout"))
	}
	//: a usable policy.
	return nil
}
