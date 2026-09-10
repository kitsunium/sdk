// Package health — hosts Config, the registry's construction parameters, and
// the two bounds the SDK will not let a caller past.
package health

import (
	"time"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
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
// registration above it is REFUSED ([StaleCacheWindow]), never clamped.
//
// Thirty seconds is three conventional poll periods. Past that, at most three
// consecutive probes could replay one measurement — which is already longer
// than an operator watching a probe recover would wait before concluding it
// had not. A cached answer is a dated statement; beyond this bound it is
// simply a claim the SDK cannot support, and quietly serving 30s to a caller
// who asked for five minutes would be the same lie in a smaller size.
const MaxCacheAge time.Duration = 30 * time.Second

// Config parameterises [New]. Every field is optional and the zero value
// builds a working registry — one that runs on the wall clock, gives each
// check [DefaultCheckTimeout], observes nothing and notifies nobody.
//
// A registry with no checks at all is a legitimate registry (ADR 0031): every
// probe answers healthy, because the process answering IS the evidence that it
// is running.
type Config struct {
	// Clock is the time source the registry reads AND waits on. A nil Clock
	// falls back to clock.System.
	//
	// It is clock.Timed rather than clock.Clock because this engine does both
	// halves: it stamps every result and it waits out every check budget.
	// Reading package time directly instead would make every timeout
	// assertion in the suite a sleep, a sleep a tolerance, and a tolerance a
	// flake somebody eventually deletes — taking the guard on "a slow
	// dependency must not restart the process" with it.
	Clock clock.Timed
	// DefaultTimeout is the budget a check gets when its own Timeout is
	// non-positive. A non-positive value here clamps in turn to
	// [DefaultCheckTimeout].
	//
	// It CLAMPS rather than refuses because the fallback is not arbitrary: it
	// is a documented, exported constant tied to a stated argument about poll
	// periods. What must never happen is the third reading — that a zero
	// means "no time at all" — which would turn a forgotten line into a probe
	// where every check fails before it runs (ADR 0031).
	DefaultTimeout time.Duration
	// OnReport observes every probe the registry answers, after the verdict
	// is computed and before it is returned.
	//
	// Calls are SERIALISED: the hook need not be safe for concurrent use, and
	// in exchange a hook that blocks blocks the probe. Keep it short; hand
	// off to a logger or a channel rather than doing work in it.
	//
	// This is where a check's FULL error belongs — its Private half, its
	// fields, the driver's own message. None of that ever reaches an HTTP
	// body; the split is the whole point (ADR 0060 §D6).
	//
	// A nil hook is a working configuration. It does mean the reports go
	// NOWHERE — the SDK will not write them to stderr on the caller's behalf,
	// and never to stdout at all (ADR 0030).
	OnReport func(corehealth.ReportValue)
	// Notify asks for the systemd sd_notify(3) readiness protocol: READY=1
	// the first time a READINESS probe reports a serving status, and STATUS=
	// on every aggregate change after that.
	//
	// It is deliberately driven by readiness and not by startup. "Every
	// component constructed" and "this replica can serve a request" are two
	// different claims, and a supervisor that acts on the first while the
	// second is false starts routing to a process that cannot answer.
	// lifecycle.RunConfig.Notify makes the earlier claim; use one or the
	// other, not both.
	//
	// It is safe to set on a process that may not be supervised: with
	// $NOTIFY_SOCKET unset every notifier call is a documented no-op that
	// returns nil, so an unsupervised binary stays silent rather than
	// failing.
	Notify bool
	// OnNotifyError receives a [NotifyFailed] when an opt-in sd_notify
	// datagram could not be delivered.
	//
	// It is a second hook rather than a second argument to OnReport because
	// the two have different audiences: a probe verdict is for whatever
	// watches the service, and a supervisor datagram that did not arrive is
	// for whoever wired the unit file. Folding them together would make every
	// observer switch on which of the two it had been handed.
	//
	// A nil hook drops the error, for the same ADR 0030 reason OnReport does:
	// the SDK writes nothing anywhere on the caller's behalf.
	OnNotifyError func(error)
}

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
