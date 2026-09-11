// Package resilience — fallback (secondary-operation) policy.
package resilience

import (
	"context"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// fallbackRunner runs a secondary Operation when the primary one fails, and
// reports BOTH errors when the secondary fails too.
type fallbackRunner struct {
	secondary coreres.Operation
	retryable func(error) bool
}

// NewFallback returns a Runner that runs cfg.Fallback when the Operation passed
// to Run fails, and returns nil when the fallback succeeds — that masking IS
// the policy. When both fail the result is FallbackFailed, carrying both
// messages as fields so neither failure is lost.
//
// A cancelled context is reported as itself, never as FallbackFailed: when it
// is already dead after the primary the fallback does not run at all, and when
// it dies WHILE the fallback runs, a failed fallback returns ctx.Err(). A
// fallback that succeeds despite a late cancellation still returns nil — the
// work was done.
//
// A nil cfg.Fallback is refused: every call returns PolicyMisconfigured without
// running the operation (ADR 0031). A fallback policy with no fallback would
// run the primary and pass its error through unchanged — indistinguishable
// from no policy at all, while the caller believes their plan B is in place.
//
// The primary error is NOT surfaced when the fallback succeeds; that is what a
// fallback is for. A caller who needs to know how often plan B fires
// instruments the fallback Operation itself, which is an ordinary closure.
func NewFallback(cfg FallbackConfig) coreres.Runner {
	//: the secondary IS this policy — there is no SDK-side operation that is
	//: not a guess at the caller's intent, so refuse rather than invent one.
	if cfg.Fallback == nil {
		//: fail closed, and say why.
		return newMisconfigured("fallback", "Fallback")
	}
	//: a stateless value runner — safe to share.
	return fallbackRunner{secondary: cfg.Fallback, retryable: cfg.Retryable}
}

// Run executes op and, if it fails, the configured fallback.
func (f fallbackRunner) Run(ctx context.Context, op coreres.Operation) error {
	//: the primary attempt always runs first — the fallback is a consequence.
	primaryErr := op(ctx)
	//: a successful primary ends the policy; the fallback never runs.
	if primaryErr == nil {
		//: nothing to fall back from.
		return nil
	}
	//: a cancelled context would make the fallback fail the same way, and its
	//: failure would then be reported as a fallback fault rather than as the
	//: cancellation it is. Surface the cancellation instead (the retry policy
	//: stops on the same condition, for the same reason).
	if ctx.Err() != nil {
		//: the caller went away — no plan B is going to help.
		return ctx.Err()
	}
	//: a deterministic failure is the caller's own; serving a substitute answer
	//: for it hides a bug behind a stale success.
	if !isRetryable(f.retryable, primaryErr) {
		//: verbatim: no fallback, no relabel.
		return primaryErr
	}
	//: plan B, under the caller's own context.
	fallbackErr := f.secondary(ctx)
	//: a successful fallback is the whole point — the primary error is absorbed.
	//: That holds even when the caller went away while plan B was running: the
	//: work completed, and reporting a cancellation for it would tell the
	//: caller something did not happen when it did.
	if fallbackErr == nil {
		//: masked by design.
		return nil
	}
	//: the check above the fallback cannot see a cancellation that lands WHILE
	//: plan B runs — and plan B starts on whatever budget the primary left, so
	//: it is the likelier place for a deadline to expire. Plan B then fails
	//: because the context died, not because it is broken, and FALLBACK_FAILED
	//: would send an operator looking for a fault in two dependencies that did
	//: nothing wrong. Same rule as the check above, applied after the fact.
	if ctxErr := ctx.Err(); ctxErr != nil {
		//: the caller went away mid-fallback — report that, not a double fault.
		return ctxErr
	}
	//: both halves failed. Reporting only one of them makes the outcome
	//: undiagnosable — "the fallback failed" without saying what it was
	//: covering for, or "the primary failed" without saying that plan B was
	//: tried and also broke. Promoting either to the wrap origin is worse
	//: still: origin-wins would let an *errs.Error half hijack the policy's
	//: code, which is the rule wrapAs exists to enforce. So the sentinel is
	//: the origin and both messages travel as fields.
	return kerrs.Wrap(coreres.FallbackFailed, kerrs.WrapParams{},
		kerrs.String("primary", primaryErr.Error()),
		kerrs.String("fallback", fallbackErr.Error()))
}
