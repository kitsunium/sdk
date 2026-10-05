package authz

import (
	"context"

	coreauthz "github.com/kitsunium/sdk/internal/core/security/authz"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// outcomeDeny labels a refusal produced by an explicit Deny decision.
const outcomeDeny string = "deny"

// outcomeAbstain labels a refusal produced by an evaluation in which every
// policy abstained — the closed-world default, not a rule that fired.
const outcomeAbstain string = "abstain"

// outcomeUnevaluable labels a refusal produced by a policy that could not be
// evaluated: a missing attribute, a kind mismatch, a misconfiguration, or an
// error from a caller-written policy.
const outcomeUnevaluable string = "unevaluable"

// Check evaluates policy against request and reports the verdict as an error:
// nil when the request is permitted, [coreauthz.PermissionDenied] otherwise.
//
// # This is the closure, and it closes toward refusal
//
// Three different situations arrive here and all three leave as the same
// refusal:
//
//   - the policy said [coreauthz.Deny];
//   - the policy said [coreauthz.Abstain] — nothing in the policy set had an
//     opinion, which is the DEFAULT for any request the caller never wrote a
//     rule about, and which must never be read as permission;
//   - the policy could not be evaluated at all.
//
// A caller that needs to tell them apart for alerting calls the [coreauthz.Policy] and
// reads the (decision, error) pair directly; Check deliberately flattens them,
// because the party on the other side of the response must not be able to.
//
// A nil policy is not "no policy" — it is a misconfigured one, and it refuses.
// This is ADR 0031 on a FUNC port: there is no constructor to reject in, so
// the nil case has to be handled where it is used, and it has to fail closed.
func Check(ctx context.Context, policy coreauthz.Policy, request coreauthz.RequestValue) error {
	//: a nil policy would panic on call; it refuses instead, and says so.
	if policy == nil {
		//: the misconfiguration is the cause; the outcome is still a refusal.
		return denied(request, outcomeUnevaluable, coreauthz.PolicyMisconfigured)
	}
	//: one evaluation; every branch below reads its two results together.
	decision, err := policy(ctx, request)
	//: an error is absorbing — the decision beside it is deliberately ignored,
	//: so a policy that returns (Allow, err) still refuses.
	if err != nil {
		//: carry the underlying identity in fields, never in the wrap origin.
		return denied(request, outcomeUnevaluable, err)
	}
	//: a decision outside the three named states is a defect, not a verdict.
	if !decision.Valid() {
		//: report it as a misconfiguration; it is never read as permission.
		return denied(request, outcomeUnevaluable, coreauthz.PolicyMisconfigured)
	}
	//: exactly one state permits, and Granted is the only test for it.
	if decision.Granted() {
		//: permitted — the single nil return in this package.
		return nil
	}
	//: Deny and Abstain differ for the operator and not for the caller.
	return denied(request, outcomeLabel(decision), nil)
}

// outcomeLabel names a non-granting decision for the operator's field.
func outcomeLabel(decision coreauthz.Decision) string {
	//: only two states reach here; Allow returned earlier and invalid was caught.
	if decision == coreauthz.Deny {
		//: a rule fired and refused.
		return outcomeDeny
	}
	//: nothing had an opinion — the closed-world default.
	return outcomeAbstain
}

// denied builds the one refusal this package returns.
//
// The core sentinel is the wrap ORIGIN, so its code, reason, public and
// private win under errs' origin-wins rule; cause travels as fields. That
// direction is deliberate and is the whole security property: were cause the
// origin, an [coreauthz.AttributeMissing] from a condition would set the code
// a framework routes on, and its message would become the one the client sees
// — which is how a refusal starts explaining which attribute to forge.
func denied(request coreauthz.RequestValue, outcome string, cause error) error {
	//: the diagnostic set every refusal carries, on the log side only.
	fields := []errs.FieldValue{
		errs.String("outcome", outcome),
		errs.String("subject", request.Subject()),
		errs.String("action", request.Action()),
		errs.String("resource", request.Resource()),
	}
	//: a cause adds its identity — never its public message, which is already
	//: identical to the refusal's by construction.
	if cause != nil {
		//: reason first: it is what an operator greps for.
		fields = append(fields, errs.String("cause_reason", causeReason(cause)))
		//: the dotted quad routes the operator to the exact rule shape.
		fields = append(fields, errs.String("cause_code", causeCode(cause)))
	}
	//: origin-wins keeps PermissionDenied's identity and its one sentence.
	return errs.Wrap(coreauthz.PermissionDenied, errs.WrapParams{}, fields...)
}

// causeReason renders the cause's SCREAMING_SNAKE reason, falling back to its
// message for an error that is not an *errs.Error — a caller-written policy
// may return anything.
func causeReason(cause error) string {
	//: an SDK error carries a stable identifier; prefer it.
	if reason, ok := errs.ReasonOf(cause); ok {
		//: the greppable form.
		return reason
	}
	//: a foreign error has no reason; its own text is the best available.
	return cause.Error()
}

// causeCode renders the cause's dotted quad, or "-" when it has none.
func causeCode(cause error) string {
	//: an SDK error carries a routable code; a foreign one does not.
	if code, ok := errs.CodeOf(cause); ok {
		//: dotted-quad form, as ADR 0005 renders it.
		return code.String()
	}
	//: explicit absence beats an empty string in a log line.
	return "-"
}

// Must unwraps a constructor pair for a package-level var, panicking on error.
//
// It is the same shape internal/service/app/validation ships, and for the same
// reason: a policy is built once at start-up, and a misconfiguration there is
// a programming fault that must stop the process rather than be handled per
// request. Never call it on data read at run time — that turns a bad
// configuration file into a crash instead of an error.
func Must(policy coreauthz.Policy, err error) coreauthz.Policy {
	//: refuse to hand back a policy the constructor already rejected.
	if err != nil {
		//: init-time panic: the binary is not fit to serve.
		panic(err)
	}
	//: the constructor accepted it; pass it through unchanged.
	return policy
}

// MustCondition unwraps a condition constructor pair for a package-level var,
// panicking on error. Same contract as [Must].
func MustCondition(condition coreauthz.Condition, err error) coreauthz.Condition {
	//: refuse to hand back a condition the constructor already rejected.
	if err != nil {
		//: init-time panic: the binary is not fit to serve.
		panic(err)
	}
	//: the constructor accepted it; pass it through unchanged.
	return condition
}
