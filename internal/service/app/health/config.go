package health

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// DefaultCheckTimeout is the per-check budget a non-positive Timeout clamps
// to.
//
// One second, and the number is defensible rather than round. A probe is
// polled on a period an orchestrator owns — Kubernetes defaults to
// periodSeconds 10 with timeoutSeconds 1 — and the SDK's own budget has to sit
// UNDER the orchestrator's, or the orchestrator gives up first and the caller
// gets no body, no attribution and no idea which check was slow.
//
// A check that legitimately needs longer than a second is a check that should
// carry a [corehealth.ReadinessCheckValue.MaxAge] and be measured off the
// probe path, not one that should be given a longer budget on it.
const DefaultCheckTimeout time.Duration = time.Second

// MaxCacheAge is the ceiling on [corehealth.ReadinessCheckValue.MaxAge]. A
// registration above it is REFUSED ([corehealth.StaleCacheWindow]), never clamped.
//
// Thirty seconds is three conventional poll periods. Past that, at most three
// consecutive probes could replay one measurement — which is already longer
// than an operator watching a probe recover would wait before concluding it
// had not. A cached answer is a dated statement; beyond this bound it is
// simply a claim the SDK cannot support, and quietly serving 30s to a caller
// who asked for five minutes would be the same lie in a smaller size.
const MaxCacheAge time.Duration = 30 * time.Second

// checkBudget resolves the budget for one check, applying both clamps.
func (c Config) checkBudget(own time.Duration) time.Duration {
	//: the check's own budget wins whenever it is a real one.
	if own > 0 {
		//: the caller was explicit about this check.
		return own
	}
	//: an unset check falls back to the registry's own default.
	if c.DefaultTimeout > 0 {
		//: the caller was explicit about the registry.
		return c.DefaultTimeout
	}
	//: neither was set — the documented floor, never zero.
	return DefaultCheckTimeout
}

// resolvedClock resolves the time source, applying the documented fallback.
func (c Config) resolvedClock() clock.Timed {
	//: the wall clock is the only non-arbitrary default here.
	if c.Clock == nil {
		//: never mutate clock.System at package scope.
		return clock.System
	}
	//: the caller's injected clock.
	return c.Clock
}
