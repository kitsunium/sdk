// Package scheduler — hosts Config, the engine's construction parameters.
package scheduler

import (
	coresched "github.com/kitsunium/sdk/internal/core/scheduler"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// Config parameterises [New]. Both fields are optional and the zero value
// builds a working scheduler — one that runs on the wall clock and reports
// nothing.
type Config struct {
	// Clock is the time source the engine reads AND waits on. A nil Clock
	// falls back to clock.System.
	//
	// It is clock.Timed rather than clock.Clock because this engine does both
	// halves: it stamps results and it waits for deadlines. Depending on
	// package time directly instead would make every cadence assertion in the
	// test suite a sleep, which is the claim ADR 0039 exists to make true.
	Clock clock.Timed
	// OnResult observes every decision the scheduler takes — each run, with
	// the job's own error, and each fire skipped for overlap.
	//
	// Calls are SERIALISED: the hook need not be safe for concurrent use, and
	// in exchange a hook that blocks blocks the scheduler. Keep it short; hand
	// off to a logger or a channel rather than doing work in it.
	//
	// A nil hook is a working configuration, not a refusal: a caller whose
	// jobs log their own failures has nothing to observe here. It does mean
	// the job's error goes NOWHERE — the SDK will not write it to stderr on
	// the caller's behalf (ADR 0030) — so a caller who wants to know their
	// jobs are failing must wire this.
	OnResult func(coresched.ResultValue)
}
