// Package session — the in-memory store's construction parameters.
package session

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	coresession "github.com/kitsunium/sdk/internal/core/session"
)

// Config configures a store's two deadlines and its clock.
//
// # Both timeouts are required, and 0 is refused
//
// ADR 0031: a policy constructor never returns an inert policy. A zero timeout
// read as "expires immediately" would produce a store in which every session is
// already dead — a login loop with no error message anywhere, which is the
// worst possible way to present a misconfiguration. There is also no default
// the SDK could supply without inventing the caller's security requirement: how
// long a session may idle, and how long it may live at all, are the whole
// content of a session policy. So both are refused when non-positive, with
// InvalidConfig naming the field.
//
// # The idle window must be SHORTER than the absolute ceiling
//
// The effective deadline is always the earlier of the two. An idle window equal
// to or longer than the absolute ceiling can therefore never be the earlier
// one: it would never bite, and the caller would believe they had configured a
// sliding expiry they do not have. That is the same inert-policy failure ADR
// 0031 describes, so it is refused at construction rather than accepted and
// ignored.
type Config struct {
	// IdleTimeout is how long a session may go untouched before it dies. Every
	// successful Load slides it forward from "now".
	IdleTimeout time.Duration
	// AbsoluteTimeout is how long a session may live at all, measured from the
	// instant its identifier was minted. Nothing extends it — not a Load, not
	// a Save. It is the ceiling the sliding window is clamped to.
	AbsoluteTimeout time.Duration
	// Clock is the time source. A nil Clock falls back to clock.System; a
	// clock.ManualClock makes every expiry assertion deterministic and
	// instant, which is why no test in this package sleeps.
	Clock clock.Clock
}

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
