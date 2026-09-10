package authz_test

import (
	"errors"
	"testing"

	coreauthz "github.com/kitsunium/sdk/internal/core/authz"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcauthz "github.com/kitsunium/sdk/internal/service/authz"
)

// holds is a condition that always holds; it names no attribute, so it can be
// used to isolate the rule machinery from the attribute machinery.
func holds(_ coreauthz.RequestValue) (bool, error) {
	//: the SDK ships no Always condition on purpose; a test may write one.
	return true, nil
}

// declines is a condition that is evaluated and does not hold.
func declines(_ coreauthz.RequestValue) (bool, error) {
	//: an answer, not a failure — the rule leaves the decision to the others.
	return false, nil
}

// unevaluable is a condition that could not be evaluated at all.
func unevaluable(_ coreauthz.RequestValue) (bool, error) {
	//: the shape a missing attribute produces, without needing a request.
	return false, coreauthz.AttributeMissing
}

// TestABACFiresItsEffect covers both effects on a matching, holding rule.
func TestABACFiresItsEffect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		effect coreauthz.Decision
	}{
		{"an allow rule permits", coreauthz.Allow},
		{"a deny rule refuses", coreauthz.Deny},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			policy := svcauthz.Must(svcauthz.NewABAC(svcauthz.RuleValue{
				Name: "r", Action: "read", Resource: "doc", Effect: tc.effect, When: holds,
			}))
			decision, err := policy(t.Context(), anyRequest())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if decision != tc.effect {
				t.Fatalf("decision = %v, want %v", decision, tc.effect)
			}
		})
	}
}

// TestARuleThatDeclinesIsNotARefusal is the abstention rule inside the
// evaluator. A condition that answered false has decided nothing.
func TestARuleThatDeclinesIsNotARefusal(t *testing.T) {
	t.Parallel()
	policy := svcauthz.Must(svcauthz.NewABAC(svcauthz.RuleValue{
		Name: "r", Action: "read", Resource: "doc", Effect: coreauthz.Deny, When: declines,
	}))
	decision, err := policy(t.Context(), anyRequest())
	if err != nil || decision != coreauthz.Abstain {
		t.Fatalf("decision = %v err = %v, want abstain and no error", decision, err)
	}
}

// TestARuleForAnotherActionIsNotEvaluated pins that a rule's condition never
// runs on a request the rule was not written for. It is what makes a condition
// safe to write against attributes only its own action carries.
func TestARuleForAnotherActionIsNotEvaluated(t *testing.T) {
	t.Parallel()
	ran := false
	policy := svcauthz.Must(svcauthz.NewABAC(svcauthz.RuleValue{
		Name: "r", Action: "write", Resource: "doc", Effect: coreauthz.Deny,
		When: func(coreauthz.RequestValue) (bool, error) {
			ran = true
			return true, nil
		},
	}))
	decision, err := policy(t.Context(), anyRequest())
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	if ran {
		t.Fatal("a rule about another action evaluated its condition")
	}
	if decision != coreauthz.Abstain {
		t.Fatalf("decision = %v, want abstain", decision)
	}
}

// TestDenyOverridesInsideTheRuleSetToo pins that nesting is invisible: the
// rules of one ABAC policy fold by the same algorithm the policy set folds by,
// so grouping rules into one policy or several changes nothing.
func TestDenyOverridesInsideTheRuleSetToo(t *testing.T) {
	t.Parallel()
	allow := svcauthz.RuleValue{Name: "a", Action: "read", Resource: "doc", Effect: coreauthz.Allow, When: holds}
	deny := svcauthz.RuleValue{Name: "d", Action: "read", Resource: "doc", Effect: coreauthz.Deny, When: holds}
	together := svcauthz.Must(svcauthz.NewABAC(allow, deny))
	reversed := svcauthz.Must(svcauthz.NewABAC(deny, allow))
	split := svcauthz.DenyOverrides(
		svcauthz.Must(svcauthz.NewABAC(allow)),
		svcauthz.Must(svcauthz.NewABAC(deny)))
	for name, policy := range map[string]coreauthz.Policy{
		"one policy": together, "reversed": reversed, "two policies": split,
	} {
		decision, err := policy(t.Context(), anyRequest())
		if err != nil || decision != coreauthz.Deny {
			t.Errorf("%s: decision = %v err = %v, want deny", name, decision, err)
		}
	}
}

// TestUnevaluableRefusesWhateverTheEffect is decision four, end to end inside
// the evaluator. It is asserted for BOTH effects because the tempting shortcut
// — "the rule would have denied anyway, so the error does not matter" — is
// wrong in the Allow direction and right for the wrong reason in the other.
func TestUnevaluableRefusesWhateverTheEffect(t *testing.T) {
	t.Parallel()
	for _, effect := range []coreauthz.Decision{coreauthz.Allow, coreauthz.Deny} {
		policy := svcauthz.Must(svcauthz.NewABAC(svcauthz.RuleValue{
			Name: "r", Action: "read", Resource: "doc", Effect: effect, When: unevaluable,
		}))
		decision, err := policy(t.Context(), anyRequest())
		if decision != coreauthz.Deny {
			t.Errorf("effect %v: decision = %v, want deny", effect, decision)
		}
		if !errs.HasCode(err, coreauthz.CodeAttributeMissing) {
			t.Errorf("effect %v: err = %v, want ATTRIBUTE_MISSING", effect, err)
		}
	}
}

// TestUnevaluableIsNotSwallowedByASiblingGrant is the composition half of the
// same rule: a rule that DID grant must not cover for one that could not be
// evaluated at all.
func TestUnevaluableIsNotSwallowedByASiblingGrant(t *testing.T) {
	t.Parallel()
	policy := svcauthz.Must(svcauthz.NewABAC(
		svcauthz.RuleValue{Name: "grant", Action: "read", Resource: "doc", Effect: coreauthz.Allow, When: holds},
		svcauthz.RuleValue{Name: "broken", Action: "read", Resource: "doc", Effect: coreauthz.Allow, When: unevaluable},
	))
	decision, err := policy(t.Context(), anyRequest())
	if decision != coreauthz.Deny || err == nil {
		t.Fatalf("decision = %v err = %v, want deny with the underlying failure", decision, err)
	}
}

// TestRuleSetRefusalsRunAtConstruction covers every shape that could never
// decide correctly, including the one an unset field produces: Effect defaults
// to Abstain, and a rule that abstains when it fires does nothing.
func TestRuleSetRefusalsRunAtConstruction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		rules []svcauthz.RuleValue
	}{
		{"no rules", nil},
		{"unnamed rule", []svcauthz.RuleValue{{
			Action: "read", Resource: "doc", Effect: coreauthz.Allow, When: holds,
		}}},
		{"empty action", []svcauthz.RuleValue{{
			Name: "r", Resource: "doc", Effect: coreauthz.Allow, When: holds,
		}}},
		{"empty resource", []svcauthz.RuleValue{{
			Name: "r", Action: "read", Effect: coreauthz.Allow, When: holds,
		}}},
		{"nil condition", []svcauthz.RuleValue{{
			Name: "r", Action: "read", Resource: "doc", Effect: coreauthz.Allow,
		}}},
		{"unset effect is Abstain", []svcauthz.RuleValue{{
			Name: "r", Action: "read", Resource: "doc", When: holds,
		}}},
		{"out-of-contract effect", []svcauthz.RuleValue{{
			Name: "r", Action: "read", Resource: "doc", Effect: coreauthz.Decision(9), When: holds,
		}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			policy, err := svcauthz.NewABAC(tc.rules...)
			if policy != nil {
				t.Fatal("a refused rule set still produced a policy")
			}
			if !errs.HasCode(err, svcauthz.CodeRuleInvalid) {
				t.Fatalf("err = %v, want RULE_INVALID", err)
			}
		})
	}
}

// TestMustPanicsOnARefusedConstructor pins the init-time contract: a policy
// that could not be built stops the process rather than serving.
func TestMustPanicsOnARefusedConstructor(t *testing.T) {
	t.Parallel()
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("Must accepted a refused constructor")
		}
		failure, ok := recovered.(error)
		if !ok || !errs.HasCode(failure, svcauthz.CodeRuleInvalid) {
			t.Fatalf("panic value = %v, want the RULE_INVALID error", recovered)
		}
	}()
	svcauthz.Must(svcauthz.NewABAC())
}

// TestRulesAreIsolatedFromLaterMutation pins the defensive copy of the rule
// set: a caller editing the slice it passed must not be able to flip an
// effect on a policy that is already serving.
func TestRulesAreIsolatedFromLaterMutation(t *testing.T) {
	t.Parallel()
	rules := []svcauthz.RuleValue{{
		Name: "r", Action: "read", Resource: "doc", Effect: coreauthz.Allow, When: holds,
	}}
	policy := svcauthz.Must(svcauthz.NewABAC(rules...))
	rules[0].Effect = coreauthz.Deny
	decision, err := policy(t.Context(), anyRequest())
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	if decision != coreauthz.Allow {
		t.Fatalf("decision = %v, want allow — the live policy read a mutated rule", decision)
	}
}

// TestAForeignErrorFromAConditionStillRefuses covers a caller-written
// condition that returns an ordinary error rather than an SDK sentinel.
func TestAForeignErrorFromAConditionStillRefuses(t *testing.T) {
	t.Parallel()
	boom := errors.New("attribute source unreachable")
	policy := svcauthz.Must(svcauthz.NewABAC(svcauthz.RuleValue{
		Name: "r", Action: "read", Resource: "doc", Effect: coreauthz.Allow,
		When: func(coreauthz.RequestValue) (bool, error) { return true, boom },
	}))
	decision, err := policy(t.Context(), anyRequest())
	if decision != coreauthz.Deny || !errors.Is(err, boom) {
		t.Fatalf("decision = %v err = %v, want deny carrying the foreign error", decision, err)
	}
}
