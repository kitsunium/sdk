// Package resilience — hedging policy configuration.
package resilience

import "time"

// HedgeConfig parameterises NewHedge. Three of its four knobs have no default:
// Idempotent, Delay and MaxInFlight are refused when unset, because every value
// the SDK could pick for them is either a guess at the caller's requirement or
// a licence to duplicate load the caller never agreed to (ADR 0031). MaxHedges
// is the one with an obvious floor and clamps to 1.
type HedgeConfig struct {
	// Idempotent asserts that the guarded Operation is safe to run more than
	// once, concurrently, with at most one of the copies being observed. It
	// has no default: false — the zero value — makes NewHedge return a policy
	// that refuses every call with PolicyMisconfigured.
	//
	// This is the one precondition in the package that is a property of the
	// CALLER'S CODE rather than of a number, and the SDK has no way to detect
	// it: a hedged non-idempotent Operation does not fail, it succeeds twice —
	// a double charge, a double insert, a duplicate outbound message — and
	// nothing in the policy or its errors will ever mention it. A doc comment
	// is the wrong instrument for a hazard whose symptom is silent success, so
	// the claim is made in code, at the construction site, where review sees
	// it and `grep -r 'Idempotent:'` enumerates every hedged call path in a
	// codebase. Requiring it (rather than defaulting it to true) also means a
	// caller cannot arrive at hedging by copying a config and deleting the
	// field.
	Idempotent bool
	// Delay is how long the policy waits for an outstanding attempt before
	// issuing a duplicate. It has no default: a non-positive Delay makes
	// NewHedge return a policy that refuses every call with
	// PolicyMisconfigured.
	//
	// Zero is the value that matters, because it is what forgetting the field
	// yields, and a zero-delay hedge duplicates EVERY call the instant it
	// starts — a load amplifier wearing a resilience policy's name, doing the
	// exact opposite of its job on the dependency it is supposed to protect.
	// It is refused rather than clamped for the same reason as
	// RateLimiterConfig.Rate: the useful value is the caller's own latency
	// distribution (a p95/p99 they measured), and an SDK-chosen one would
	// either hedge constantly or never.
	Delay time.Duration
	// MaxHedges is how many DUPLICATE attempts a single call may issue beyond
	// the first, one per Delay elapsed. It clamps to 1: a hedging policy that
	// may issue no duplicate is inert — it would run the primary attempt and
	// nothing else while claiming a tail-latency protection it does not
	// provide — and "issue at least one duplicate" is exactly the kind of
	// obvious floor that ADR 0031 clamps rather than refuses (cf. Burst,
	// NewBulkhead's limit).
	MaxHedges int
	// MaxInFlight bounds how many duplicate attempts this Runner may have
	// running at once, across all concurrent calls. It has no default: a
	// non-positive MaxInFlight makes NewHedge return a policy that refuses
	// every call with PolicyMisconfigured.
	//
	// It is the answer to hedging's second hazard: a dependency that has gone
	// slow makes every in-flight call hedge at the same moment, so the policy
	// adds load precisely when the dependency has least to spare and deepens
	// the outage it was meant to hide. The cap counts duplicates only — the
	// first attempt of every call always runs, so reaching the cap degrades
	// the policy to no-hedging rather than rejecting the call. A caller who
	// wants rejection under saturation composes NewBulkhead, whose job that
	// is.
	//
	// It is refused rather than defaulted because both directions of a guess
	// are harmful in this specific knob, which is the rarer case where neither
	// clamping nor defaulting has a safe side: a conservative SDK default
	// silently stops hedging under exactly the load the caller bought hedging
	// for (inert, ADR 0031's own failure mode), and a generous one hands back
	// the amplifier. The number is a capacity decision about the caller's
	// downstream, and only they hold it.
	MaxInFlight int
}
