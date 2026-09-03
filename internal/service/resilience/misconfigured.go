// Package resilience — the Runner returned when a policy cannot be honoured.
package resilience

import (
	"context"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// misconfiguredRunner refuses every call with PolicyMisconfigured instead of
// running the operation. It is what a constructor returns when it was handed a
// value it cannot honour and no default would be anything but a guess (ADR
// 0031): the alternative is an inert policy that reports its own failure as if
// it were normal operation, which is the defect the ADR exists to prevent.
//
// The refusal surfaces at first use rather than at construction because the
// constructors return a bare Runner. Changing them to (Runner, error) would
// break every call site to report a fault none of them has.
type misconfiguredRunner struct {
	policy string
	knob   string
}

// newMisconfigured returns a Runner that refuses every call, naming the policy
// and the offending knob. The value itself is never carried — the field says
// which knob is wrong, not what was passed.
func newMisconfigured(policy, knob string) coreres.Runner {
	//: a stateless refusal — safe to share.
	return misconfiguredRunner{policy: policy, knob: knob}
}

// Run refuses the call without invoking op.
func (m misconfiguredRunner) Run(_ context.Context, _ coreres.Operation) error {
	//: the operation is never run — a misconfigured policy is fail-closed, and
	//: running the work unguarded would be the inert behaviour ADR 0031 bans.
	return kerrs.Wrap(coreres.PolicyMisconfigured, kerrs.WrapParams{},
		kerrs.String("policy", m.policy), kerrs.String("knob", m.knob))
}
