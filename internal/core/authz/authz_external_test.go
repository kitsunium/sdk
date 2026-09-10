package authz_test

import (
	"reflect"
	"testing"

	coreauthz "github.com/kitsunium/sdk/internal/core/authz"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestPortsAreFunctionsNotInterfaces is the executable form of ADR 0039's
// rule. A published interface cannot grow a method without breaking every
// downstream implementer at compile time; a func type cannot grow one at all.
// Turning Policy or Condition into an interface fails here rather than in a
// consumer's build.
func TestPortsAreFunctionsNotInterfaces(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind reflect.Kind
	}{
		{"Policy", reflect.TypeFor[coreauthz.Policy]().Kind()},
		{"Condition", reflect.TypeFor[coreauthz.Condition]().Kind()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.kind != reflect.Func {
				t.Errorf("%s must stay a func type (ADR 0039), got kind %v", tc.name, tc.kind)
			}
		})
	}
}

// TestZeroDecisionIsAbstainAndGrantsNothing pins the ADR 0031 half that a FUNC
// port cannot express any other way: there is no constructor to refuse in, so
// the zero value has to be the safe one.
//
// A policy that forgets to set its result must not authorize, and must not
// veto every other policy either. Abstain is the only value that does neither.
func TestZeroDecisionIsAbstainAndGrantsNothing(t *testing.T) {
	t.Parallel()
	var zero coreauthz.Decision
	if zero != coreauthz.Abstain {
		t.Fatalf("zero Decision = %v, want Abstain — a zero that allowed would be a vulnerability", zero)
	}
	if zero.Granted() {
		t.Fatal("zero Decision grants; the zero value must never authorize")
	}
	if coreauthz.Allow == 0 || coreauthz.Deny == 0 {
		t.Fatal("Allow or Deny is the zero value; only Abstain may be")
	}
}

// TestGrantedIsAllowOnly pins the accessor that exists so no call site writes
// `!= Deny`, which is true for Abstain and therefore authorizes every request
// no policy recognised.
func TestGrantedIsAllowOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		decision coreauthz.Decision
		want     bool
	}{
		{"allow grants", coreauthz.Allow, true},
		{"deny does not", coreauthz.Deny, false},
		{"abstain does not", coreauthz.Abstain, false},
		{"out of contract does not", coreauthz.Decision(200), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.decision.Granted(); got != tc.want {
				t.Errorf("Granted() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestValidAndStringRefuseToDressUpACorruptDecision covers the case a numeric
// conversion or a zeroed struct field can produce. Neither the predicate nor
// the rendering may present it as one of the three real states.
func TestValidAndStringRefuseToDressUpACorruptDecision(t *testing.T) {
	t.Parallel()
	corrupt := coreauthz.Decision(9)
	if corrupt.Valid() {
		t.Fatal("Decision(9).Valid() is true; only the three named states are valid")
	}
	if got := corrupt.String(); got != "invalid" {
		t.Fatalf("Decision(9).String() = %q, want %q", got, "invalid")
	}
	names := map[coreauthz.Decision]string{
		coreauthz.Abstain: "abstain",
		coreauthz.Allow:   "allow",
		coreauthz.Deny:    "deny",
	}
	for decision, want := range names {
		if !decision.Valid() {
			t.Errorf("%s must be Valid", want)
		}
		if got := decision.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}

// TestPresentAndFalseIsNotAbsent is the map[string]string defect, executable.
// The two facts the string map cannot tell apart are the two this domain
// refuses to conflate, and a flag is where the conflation is most expensive.
func TestPresentAndFalseIsNotAbsent(t *testing.T) {
	t.Parallel()
	request := coreauthz.NewRequestValue("u-1", "read", "doc", coreauthz.AttrBool("mfa", false))
	attr, ok := request.Attr("mfa")
	if !ok {
		t.Fatal("a flag set to false must be PRESENT")
	}
	value, kindOK := attr.BoolValue()
	if !kindOK {
		t.Fatal("BoolValue reported a kind mismatch on a bool attribute")
	}
	if value {
		t.Fatal("the flag was stored as true")
	}
	if _, present := request.Attr("nope"); present {
		t.Fatal("an attribute that was never set reports as present")
	}
}

// TestAccessorsReportKindMismatchRatherThanAZero covers the second half of the
// same rule. A text rule reading a numeric attribute must learn that it did
// not compare — not receive an empty string it would then compare against.
func TestAccessorsReportKindMismatchRatherThanAZero(t *testing.T) {
	t.Parallel()
	number := coreauthz.AttrInt64("age", 30)
	if _, ok := number.StringValue(); ok {
		t.Error("StringValue accepted an int64 attribute")
	}
	if _, ok := number.BoolValue(); ok {
		t.Error("BoolValue accepted an int64 attribute")
	}
	if _, ok := number.StringsValue(); ok {
		t.Error("StringsValue accepted an int64 attribute")
	}
	if _, ok := number.Contains("30"); ok {
		t.Error("Contains accepted an int64 attribute")
	}
	value, ok := number.Int64Value()
	if !ok || value != 30 {
		t.Errorf("Int64Value = (%d, %v), want (30, true)", value, ok)
	}
}

// TestSetAttributeIsClonedBothWays pins the immutability an attribute needs to
// be shared by every goroutine evaluating a policy. A caller that keeps the
// slice it passed, or sorts the one it got back, must not be able to rewrite
// the roles the next request is checked against.
func TestSetAttributeIsClonedBothWays(t *testing.T) {
	t.Parallel()
	source := []string{"editor", "reviewer"}
	attr := coreauthz.AttrStrings("roles", source...)
	source[0] = "admin"
	if held, _ := attr.Contains("admin"); held {
		t.Fatal("mutating the caller's slice changed a live attribute")
	}
	out, ok := attr.StringsValue()
	if !ok {
		t.Fatal("StringsValue rejected a strings attribute")
	}
	out[0] = "admin"
	if held, _ := attr.Contains("admin"); held {
		t.Fatal("mutating the returned slice changed a live attribute")
	}
}

// TestEmptySetIsAFactAndAbsenceIsNot separates "this subject holds no roles"
// from "nobody said". The first is a legitimate anonymous request; the second
// is a producer bug, and every rule that names the attribute refuses on it.
func TestEmptySetIsAFactAndAbsenceIsNot(t *testing.T) {
	t.Parallel()
	request := coreauthz.NewRequestValue("anon", "read", "doc", coreauthz.AttrStrings("roles"))
	attr, ok := request.Attr("roles")
	if !ok {
		t.Fatal("an empty set attribute must be present")
	}
	if attr.Kind() != coreauthz.KindStrings {
		t.Fatalf("kind = %v, want KindStrings", attr.Kind())
	}
	if held, kindOK := attr.Contains("editor"); held || !kindOK {
		t.Fatalf("Contains on an empty set = (%v, %v), want (false, true)", held, kindOK)
	}
}

// TestUnusableAttributesAreDroppedNotStored pins the constructor invariant
// that keeps "found but unusable" out of every rule: an attribute with no name
// or no kind becomes ABSENT, and absence is already a refusal.
func TestUnusableAttributesAreDroppedNotStored(t *testing.T) {
	t.Parallel()
	request := coreauthz.NewRequestValue("u-1", "read", "doc",
		coreauthz.AttrString("", "nameless"),
		coreauthz.AttrValue{},
		coreauthz.AttrString("dept", "finance"))
	if got := request.AttrCount(); got != 1 {
		t.Fatalf("AttrCount = %d, want 1 — only the usable attribute is stored", got)
	}
	if _, ok := request.Attr(""); ok {
		t.Error("an attribute with an empty key was stored")
	}
	attr, ok := request.Attr("dept")
	if !ok || attr.Kind() != coreauthz.KindString {
		t.Errorf("the usable attribute did not survive: ok=%v kind=%v", ok, attr.Kind())
	}
}

// TestLastAttributeWins documents the layering a caller relies on when it
// applies defaults and then overrides them per request.
func TestLastAttributeWins(t *testing.T) {
	t.Parallel()
	request := coreauthz.NewRequestValue("u-1", "read", "doc",
		coreauthz.AttrString("tier", "free"),
		coreauthz.AttrString("tier", "paid"))
	attr, _ := request.Attr("tier")
	got, _ := attr.StringValue()
	if got != "paid" {
		t.Fatalf("tier = %q, want %q", got, "paid")
	}
}

// TestRequestIsImmutableThroughItsAccessors keeps the three strings readable
// and unwritable, which is what lets one request value be handed to every
// policy in a composition.
func TestRequestIsImmutableThroughItsAccessors(t *testing.T) {
	t.Parallel()
	request := coreauthz.NewRequestValue("u-7", "publish", "article")
	if request.Subject() != "u-7" || request.Action() != "publish" || request.Resource() != "article" {
		t.Fatalf("accessors disagree with the constructor: %q %q %q",
			request.Subject(), request.Action(), request.Resource())
	}
	if fields := reflect.TypeFor[coreauthz.RequestValue]().NumField(); fields != 4 {
		t.Fatalf("RequestValue has %d fields; every one of them must stay unexported", fields)
	}
	for index := range reflect.TypeFor[coreauthz.RequestValue]().NumField() {
		if reflect.TypeFor[coreauthz.RequestValue]().Field(index).IsExported() {
			t.Errorf("RequestValue field %d is exported; the value would stop being immutable",
				index)
		}
	}
}

// TestEveryRefusalShowsTheSameSentence is the security property this domain is
// built around, asserted rather than described.
//
// A refusal message that explains itself is a description of the policy set
// handed to the party the policy exists to keep out. All four outcomes — an
// explicit refusal, a missing attribute, a kind mismatch, a misconfiguration —
// therefore render the SAME Public, and each keeps a distinct Private.
func TestEveryRefusalShowsTheSameSentence(t *testing.T) {
	t.Parallel()
	sentinels := []*errs.Error{
		coreauthz.PermissionDenied,
		coreauthz.AttributeMissing,
		coreauthz.AttributeKindMismatch,
		coreauthz.PolicyMisconfigured,
	}
	want := coreauthz.PermissionDenied.Public()
	seen := make(map[string]struct{}, len(sentinels))
	for _, sentinel := range sentinels {
		if got := sentinel.Public(); got != want {
			t.Errorf("%s public = %q, want %q — a refusal must not describe itself",
				sentinel.Reason(), got, want)
		}
		if sentinel.HTTPStatus() != 403 {
			t.Errorf("%s status = %d, want 403", sentinel.Reason(), sentinel.HTTPStatus())
		}
		if _, duplicate := seen[sentinel.Private()]; duplicate {
			t.Errorf("%s shares a Private with another sentinel; the diagnosis must stay distinct",
				sentinel.Reason())
		}
		seen[sentinel.Private()] = struct{}{}
	}
}

// TestPublicNamesNoAttributeRoleOrRule guards the sentence itself against the
// obvious regression: someone "improving" the message by saying why.
func TestPublicNamesNoAttributeRoleOrRule(t *testing.T) {
	t.Parallel()
	forbidden := []string{"role", "admin", "attribute", "rule", "policy", "missing", "subject"}
	public := coreauthz.PermissionDenied.Public()
	for _, word := range forbidden {
		if containsFold(public, word) {
			t.Errorf("public message %q names %q; it must describe no part of the policy model",
				public, word)
		}
	}
}

// containsFold reports a case-insensitive substring match without pulling a
// regexp in for four words.
func containsFold(haystack, needle string) bool {
	//: both sides are ASCII literals in this test, so a byte-wise fold is exact.
	lower := []byte(haystack)
	for index, char := range lower {
		if char >= 'A' && char <= 'Z' {
			lower[index] = char + ('a' - 'A')
		}
	}
	return len(needle) > 0 && indexOf(string(lower), needle) >= 0
}

// indexOf is strings.Index, inlined so the helper above needs no import that
// the rest of the file does not use.
func indexOf(haystack, needle string) int {
	//: naive scan; the inputs are one short sentence and one word.
	for start := 0; start+len(needle) <= len(haystack); start++ {
		if haystack[start:start+len(needle)] == needle {
			return start
		}
	}
	return -1
}
