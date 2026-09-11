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
	if fallbackErr == nil {
		//: masked by design.
		return nil
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
