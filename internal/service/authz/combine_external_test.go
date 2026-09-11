package authz_test

import (
	"context"
	"errors"
	"testing"

	coreauthz "github.com/kitsunium/sdk/internal/core/authz"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcauthz "github.com/kitsunium/sdk/internal/service/authz"
)

// constant returns a policy that always answers decision.
func constant(decision coreauthz.Decision) coreauthz.Policy {
	//: a closure over one value; it never inspects the request.
	return func(context.Context, coreauthz.RequestValue) (coreauthz.Decision, error) {
		return decision, nil
	}
}

// failing returns a policy that cannot be evaluated.
func failing(err error) coreauthz.Policy {
	//: it answers Allow beside the error on purpose — the fold must ignore it.
	return func(context.Context, coreauthz.RequestValue) (coreauthz.Decision, error) {
		return coreauthz.Allow, err
	}
}

// probe returns a policy that records that it ran.
func probe(ran *bool, decision coreauthz.Decision) coreauthz.Policy {
	//: the flag is read after a single-goroutine evaluation, so no lock.
	return func(context.Context, coreauthz.RequestValue) (coreauthz.Decision, error) {
		*ran = true
		return decision, nil
	}
}

// anyRequest is the request every combiner test uses; the combiner never reads
// it, which is itself part of the contract.
func anyRequest() coreauthz.RequestValue {
	//: no attributes — the combiner must not need any.
	return coreauthz.NewRequestValue("u-1", "read", "doc")
}

// TestDenyBeatsAllowInEveryOrder is the combining rule, asserted over every
// permutation. A first-applicable implementation wearing this function's name
// would pass the {allow, deny} case and fail {deny, allow} — or the reverse —
// so the permutation is the test, not a nicety.
func TestDenyBeatsAllowInEveryOrder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		members []coreauthz.Policy
	}{
		{"allow then deny", []coreauthz.Policy{constant(coreauthz.Allow), constant(coreauthz.Deny)}},
		{"deny then allow", []coreauthz.Policy{constant(coreauthz.Deny), constant(coreauthz.Allow)}},
		{
			"allow surrounded by abstentions and one deny",
			[]coreauthz.Policy{
				constant(coreauthz.Abstain), constant(coreauthz.Allow),
				constant(coreauthz.Abstain), constant(coreauthz.Deny),
			},
		},
		{
			"deny first among many",
			[]coreauthz.Policy{
				constant(coreauthz.Deny), constant(coreauthz.Allow),
				constant(coreauthz.Allow), constant(coreauthz.Allow),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			decision, err := svcauthz.DenyOverrides(tc.members...)(t.Context(), anyRequest())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if decision != coreauthz.Deny {
				t.Fatalf("decision = %v, want deny — refusal is absorbing", decision)
			}
		})
	}
}

// TestAllowDoesNotShortCircuit is what separates deny-overrides from
// first-applicable. If the fold returned on the first grant, the later member
// would never run — and a Deny sitting there would never be seen.
func TestAllowDoesNotShortCircuit(t *testing.T) {
	t.Parallel()
	ran := false
	policy := svcauthz.DenyOverrides(constant(coreauthz.Allow), probe(&ran, coreauthz.Abstain))
	if _, err := policy(t.Context(), anyRequest()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ran {
		t.Fatal("the member after an Allow was skipped; this is first-applicable, not deny-overrides")
	}
}

// TestDenyShortCircuits is the one permitted short circuit: nothing after an
// absorbing refusal can change the answer, so nothing after it is evaluated.
func TestDenyShortCircuits(t *testing.T) {
	t.Parallel()
	ran := false
	policy := svcauthz.DenyOverrides(constant(coreauthz.Deny), probe(&ran, coreauthz.Allow))
	if _, err := policy(t.Context(), anyRequest()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ran {
		t.Fatal("a member after a Deny was evaluated; refusal must be absorbing")
	}
}

// TestEmptyCompositionAbstainsAndCheckRefusesIt pins the identity of the fold
// AND the closure that makes the identity safe. The pairing is the point: the
// combiner stays algebraic, and exactly one function closes the world.
func TestEmptyCompositionAbstainsAndCheckRefusesIt(t *testing.T) {
	t.Parallel()
	policy := svcauthz.DenyOverrides()
	decision, err := policy(t.Context(), anyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != coreauthz.Abstain {
		t.Fatalf("decision = %v, want abstain — the identity of the combiner", decision)
	}
	if err := svcauthz.Check(t.Context(), policy, anyRequest()); err == nil {
		t.Fatal("an authorizer with no policy permitted a request")
	}
}

// TestNestingIsInvisible pins associativity. It is what lets a caller group
// policies for readability without changing the verdict.
func TestNestingIsInvisible(t *testing.T) {
	t.Parallel()
	flat := svcauthz.DenyOverrides(constant(coreauthz.Allow), constant(coreauthz.Abstain), constant(coreauthz.Deny))
	nested := svcauthz.DenyOverrides(constant(coreauthz.Allow),
		svcauthz.DenyOverrides(constant(coreauthz.Abstain), constant(coreauthz.Deny)))
	flatDecision, flatErr := flat(t.Context(), anyRequest())
	if flatErr != nil {
		t.Fatalf("flat: %v", flatErr)
	}
	nestedDecision, nestedErr := nested(t.Context(), anyRequest())
	if nestedErr != nil {
		t.Fatalf("nested: %v", nestedErr)
	}
	if flatDecision != nestedDecision {
		t.Fatalf("flat = %v, nested = %v; the fold must be associative", flatDecision, nestedDecision)
	}
}

// TestUnevaluableOutranksAGrant covers the ordering between the two absorbing
// events. A policy that returns (Allow, err) must not authorize: the error
// says the decision beside it is not knowledge.
func TestUnevaluableOutranksAGrant(t *testing.T) {
	t.Parallel()
	boom := errors.New("backing store unreachable")
	policy := svcauthz.DenyOverrides(constant(coreauthz.Allow), failing(boom))
	decision, err := policy(t.Context(), anyRequest())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the underlying failure", err)
	}
	if decision != coreauthz.Deny {
		t.Fatalf("decision = %v, want deny alongside the error", decision)
	}
}

// TestNilMemberRefusesRatherThanBeingSkipped guards the edit an attacker would
// most like to make to a wiring file. A skipped nil silently shrinks the
// policy set; a refusing nil is an outage that gets fixed.
func TestNilMemberRefusesRatherThanBeingSkipped(t *testing.T) {
	t.Parallel()
	policy := svcauthz.DenyOverrides(constant(coreauthz.Allow), nil)
	decision, err := policy(t.Context(), anyRequest())
	if decision != coreauthz.Deny {
		t.Fatalf("decision = %v, want deny", decision)
	}
	if !errs.HasCode(err, coreauthz.CodePolicyMisconfigured) {
		t.Fatalf("err = %v, want POLICY_MISCONFIGURED", err)
	}
}

// TestOutOfContractDecisionIsRefused covers the numeric conversion a caller's
// own code can produce. It must never be read as permission.
func TestOutOfContractDecisionIsRefused(t *testing.T) {
	t.Parallel()
	policy := svcauthz.DenyOverrides(constant(coreauthz.Decision(42)))
	decision, err := policy(t.Context(), anyRequest())
	if decision != coreauthz.Deny {
		t.Fatalf("decision = %v, want deny", decision)
	}
	if !errs.HasCode(err, coreauthz.CodePolicyMisconfigured) {
		t.Fatalf("err = %v, want POLICY_MISCONFIGURED", err)
	}
}

// TestCompositionIsIsolatedFromLaterAppends pins the defensive copy. A caller
// that keeps the slice it passed must not be able to add a policy — or drop
// one — after the composition is live.
func TestCompositionIsIsolatedFromLaterAppends(t *testing.T) {
	t.Parallel()
	members := []coreauthz.Policy{constant(coreauthz.Allow)}
	policy := svcauthz.DenyOverrides(members...)
	members[0] = constant(coreauthz.Deny)
	decision, err := policy(t.Context(), anyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != coreauthz.Allow {
		t.Fatalf("decision = %v, want allow — the composition read a mutated member", decision)
	}
}
