package authz_test

import (
	"testing"

	coreauthz "github.com/kitsunium/sdk/internal/core/authz"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcauthz "github.com/kitsunium/sdk/internal/service/authz"
)

// financeRequest carries every attribute the built-in conditions read, each
// under its correct kind.
func financeRequest() coreauthz.RequestValue {
	//: one request, four kinds, so a kind-mismatch case is always available.
	return coreauthz.NewRequestValue("u-42", "read", "doc",
		coreauthz.AttrString("department", "finance"),
		coreauthz.AttrString("author", "u-42"),
		coreauthz.AttrBool("mfa", false),
		coreauthz.AttrInt64("level", 3),
		coreauthz.AttrStrings("scopes", "read", "write"))
}

// TestConditionsHoldOnTheirOwnKind is the happy path for every built-in.
func TestConditionsHoldOnTheirOwnKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		condition coreauthz.Condition
		want      bool
	}{
		{"equal text", svcauthz.MustCondition(svcauthz.AttrEquals("department", "finance")), true},
		{"unequal text", svcauthz.MustCondition(svcauthz.AttrEquals("department", "legal")), false},
		{"flag that is off", svcauthz.MustCondition(svcauthz.AttrIsTrue("mfa")), false},
		{"number at the bound", svcauthz.MustCondition(svcauthz.AttrAtLeast("level", 3)), true},
		{"number below the bound", svcauthz.MustCondition(svcauthz.AttrAtLeast("level", 4)), false},
		{"set member", svcauthz.MustCondition(svcauthz.AttrContains("scopes", "write")), true},
		{"set non-member", svcauthz.MustCondition(svcauthz.AttrContains("scopes", "admin")), false},
		{"owner is the subject", svcauthz.MustCondition(svcauthz.AttrMatchesSubject("author")), true},
		{"owner is not the subject", svcauthz.MustCondition(svcauthz.AttrMatchesSubject("department")), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.condition(financeRequest())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("condition = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAnAbsentAttributeIsNotAFalseComparison is decision four in its most
// direct form: `department == "finance"` on a subject with no department.
//
// Answering false there would let a request carrying NO attributes at all
// satisfy every rule of that shape by omitting the field, so every built-in
// must report it as a failure instead.
func TestAnAbsentAttributeIsNotAFalseComparison(t *testing.T) {
	t.Parallel()
	bare := coreauthz.NewRequestValue("u-42", "read", "doc")
	conditions := map[string]coreauthz.Condition{
		"AttrEquals":         svcauthz.MustCondition(svcauthz.AttrEquals("department", "finance")),
		"AttrIsTrue":         svcauthz.MustCondition(svcauthz.AttrIsTrue("mfa")),
		"AttrAtLeast":        svcauthz.MustCondition(svcauthz.AttrAtLeast("level", 1)),
		"AttrContains":       svcauthz.MustCondition(svcauthz.AttrContains("scopes", "write")),
		"AttrMatchesSubject": svcauthz.MustCondition(svcauthz.AttrMatchesSubject("author")),
	}
	for name, condition := range conditions {
		holds, err := condition(bare)
		if !errs.HasCode(err, coreauthz.CodeAttributeMissing) {
			t.Errorf("%s: err = %v, want ATTRIBUTE_MISSING", name, err)
		}
		if holds {
			t.Errorf("%s: held on an absent attribute", name)
		}
	}
}

// TestAWrongKindIsNotAFalseComparisonEither generalises the same rule to the
// producer bug a string map would have hidden — the number that arrived as
// text, the set that arrived joined.
func TestAWrongKindIsNotAFalseComparisonEither(t *testing.T) {
	t.Parallel()
	request := financeRequest()
	conditions := map[string]coreauthz.Condition{
		"text rule on a number": svcauthz.MustCondition(svcauthz.AttrEquals("level", "3")),
		"flag rule on text":     svcauthz.MustCondition(svcauthz.AttrIsTrue("department")),
		"number rule on a flag": svcauthz.MustCondition(svcauthz.AttrAtLeast("mfa", 1)),
		"set rule on text":      svcauthz.MustCondition(svcauthz.AttrContains("department", "finance")),
		"owner rule on a set":   svcauthz.MustCondition(svcauthz.AttrMatchesSubject("scopes")),
	}
	for name, condition := range conditions {
		holds, err := condition(request)
		if !errs.HasCode(err, coreauthz.CodeAttributeKindMismatch) {
			t.Errorf("%s: err = %v, want ATTRIBUTE_KIND_MISMATCH", name, err)
		}
		if holds {
			t.Errorf("%s: held across a kind mismatch", name)
		}
	}
}

// TestAFlagThatIsPresentAndFalseIsAnAnswer separates the two cases the whole
// typed bag exists for. Absence fails; a flag that is off simply does not hold.
func TestAFlagThatIsPresentAndFalseIsAnAnswer(t *testing.T) {
	t.Parallel()
	condition := svcauthz.MustCondition(svcauthz.AttrIsTrue("mfa"))
	holds, err := condition(financeRequest())
	if err != nil {
		t.Fatalf("a present flag must be evaluable: %v", err)
	}
	if holds {
		t.Fatal("a flag set to false held")
	}
}

// TestNotPropagatesFailureInsteadOfInvertingIt is the negation trap.
//
// "Deny unless the department is finance" is written Not(AttrEquals(...)). If
// a missing attribute were reported as false, Not would turn it into true and
// the rule's behaviour on an attribute-less subject would depend entirely on
// which reading the implementer had in mind. Propagating the failure removes
// the choice.
func TestNotPropagatesFailureInsteadOfInvertingIt(t *testing.T) {
	t.Parallel()
	inner := svcauthz.MustCondition(svcauthz.AttrEquals("department", "finance"))
	negated := svcauthz.MustCondition(svcauthz.Not(inner))
	bare := coreauthz.NewRequestValue("u-42", "read", "doc")
	holds, err := negated(bare)
	if holds {
		t.Fatal("Not turned an unevaluable condition into a hold")
	}
	if !errs.HasCode(err, coreauthz.CodeAttributeMissing) {
		t.Fatalf("err = %v, want the inner ATTRIBUTE_MISSING, unchanged", err)
	}
	//: on a real answer it does invert.
	inverted, err := negated(financeRequest())
	if err != nil || inverted {
		t.Fatalf("Not on a holding condition = (%v, %v), want (false, nil)", inverted, err)
	}
}

// TestAnyOfIsAbsorbingOnAnUnevaluableBranch is the same defect one step out. A
// sibling branch that holds must not cover for a branch that could not be
// evaluated, or a request satisfies the rule by omitting the attribute.
func TestAnyOfIsAbsorbingOnAnUnevaluableBranch(t *testing.T) {
	t.Parallel()
	condition := svcauthz.MustCondition(svcauthz.AnyOf(
		svcauthz.MustCondition(svcauthz.AttrEquals("department", "finance")),
		svcauthz.MustCondition(svcauthz.AttrIsTrue("nonexistent")),
	))
	holds, err := condition(financeRequest())
	if holds {
		t.Fatal("AnyOf held while one branch could not be evaluated")
	}
	if !errs.HasCode(err, coreauthz.CodeAttributeMissing) {
		t.Fatalf("err = %v, want ATTRIBUTE_MISSING", err)
	}
}

// TestAllOfEvaluatesEveryBranch pins order-independence: a failure behind an
// earlier false is reported, not hidden by a short circuit.
func TestAllOfEvaluatesEveryBranch(t *testing.T) {
	t.Parallel()
	condition := svcauthz.MustCondition(svcauthz.AllOf(
		svcauthz.MustCondition(svcauthz.AttrEquals("department", "legal")),
		svcauthz.MustCondition(svcauthz.AttrIsTrue("nonexistent")),
	))
	holds, err := condition(financeRequest())
	if holds {
		t.Fatal("AllOf held")
	}
	if !errs.HasCode(err, coreauthz.CodeAttributeMissing) {
		t.Fatalf("err = %v, want the failure from the SECOND branch, not the first false", err)
	}
}

// TestCombinatorsCombine covers the ordinary logic once the failure rules are
// settled.
func TestCombinatorsCombine(t *testing.T) {
	t.Parallel()
	finance := svcauthz.MustCondition(svcauthz.AttrEquals("department", "finance"))
	legal := svcauthz.MustCondition(svcauthz.AttrEquals("department", "legal"))
	tests := []struct {
		name      string
		condition coreauthz.Condition
		want      bool
	}{
		{"all of two true", svcauthz.MustCondition(svcauthz.AllOf(finance, finance)), true},
		{"all of one false", svcauthz.MustCondition(svcauthz.AllOf(finance, legal)), false},
		{"any of one true", svcauthz.MustCondition(svcauthz.AnyOf(legal, finance)), true},
		{"any of none true", svcauthz.MustCondition(svcauthz.AnyOf(legal, legal)), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.condition(financeRequest())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("condition = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAnAnonymousRequestOwnsNothing covers the ownership rule's sharpest edge:
// an empty subject against an empty owner attribute. Equality would say yes,
// and every unowned resource would belong to every unauthenticated caller.
func TestAnAnonymousRequestOwnsNothing(t *testing.T) {
	t.Parallel()
	condition := svcauthz.MustCondition(svcauthz.AttrMatchesSubject("author"))
	request := coreauthz.NewRequestValue("", "read", "doc", coreauthz.AttrString("author", ""))
	holds, err := condition(request)
	if err != nil {
		t.Fatalf("the comparison is answerable here, so it must not fail: %v", err)
	}
	if holds {
		t.Fatal("an empty subject matched an empty owner")
	}
}

// TestConditionConstructorRefusals covers every argument a condition cannot be
// built from. Each fails at start-up, not on the request that trips over it.
func TestConditionConstructorRefusals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		build func() (coreauthz.Condition, error)
	}{
		{"AttrEquals with no key", func() (coreauthz.Condition, error) { return svcauthz.AttrEquals("", "x") }},
		{"AttrIsTrue with no key", func() (coreauthz.Condition, error) { return svcauthz.AttrIsTrue("") }},
		{"AttrAtLeast with no key", func() (coreauthz.Condition, error) { return svcauthz.AttrAtLeast("", 1) }},
		{"AttrContains with no key", func() (coreauthz.Condition, error) { return svcauthz.AttrContains("", "x") }},
		{"AttrContains with no member", func() (coreauthz.Condition, error) { return svcauthz.AttrContains("k", "") }},
		{"AttrMatchesSubject with no key", func() (coreauthz.Condition, error) { return svcauthz.AttrMatchesSubject("") }},
		{"Not of nil", func() (coreauthz.Condition, error) { return svcauthz.Not(nil) }},
		{"AllOf of nothing", func() (coreauthz.Condition, error) { return svcauthz.AllOf() }},
		{"AnyOf of nothing", func() (coreauthz.Condition, error) { return svcauthz.AnyOf() }},
		{"AllOf with a nil member", func() (coreauthz.Condition, error) { return svcauthz.AllOf(holds, nil) }},
		{"AnyOf with a nil member", func() (coreauthz.Condition, error) { return svcauthz.AnyOf(nil) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			condition, err := tc.build()
			if condition != nil {
				t.Fatal("a refused constructor still produced a condition")
			}
			if !errs.HasCode(err, svcauthz.CodeConditionInvalid) {
				t.Fatalf("err = %v, want CONDITION_INVALID", err)
			}
		})
	}
}
