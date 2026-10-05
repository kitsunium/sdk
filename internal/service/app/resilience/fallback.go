package resilience

import (
	"context"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// fallbackRunner runs a secondary Operation when the primary one fails, and
// reports BOTH errors when the secondary fails too.
type fallbackRunner struct {
	secondary coreres.Operation
	retryable func(error) bool
}

// newFallback is NewFallback's body: decl_gen.go writes NewFallback, from the
// design, as one call of it.
func newFallback(cfg FallbackConfig) coreres.Runner {
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
	//: the origin and both messages travel as fields, through the same builder.
	return wrapAsFields(coreres.FallbackFailed,
		kerrs.String("primary", primaryErr.Error()),
		kerrs.String("fallback", fallbackErr.Error()))
}
