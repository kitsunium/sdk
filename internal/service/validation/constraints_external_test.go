package validation_test

import (
	"strings"
	"testing"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcvalidation "github.com/kitsunium/sdk/internal/service/validation"
)

// secretValue is the sentinel every "a message never echoes the value" check
// feeds a constraint. It is deliberately something a real service would be
// destroyed for leaking.
const secretValue string = "hunter2-correct-horse-battery-staple"

// TestRequiredRefusesTheZeroValue pins presence, and the limitation that comes
// with it: a value type cannot tell "not supplied" from "supplied as zero".
func TestRequiredRefusesTheZeroValue(t *testing.T) {
	t.Parallel()
	constraint := svcvalidation.Required[string]()
	if report := constraint("name", "ada"); !report.OK() {
		t.Errorf("a non-zero value must be accepted, got %v", report)
	}
	report := constraint("name", "")
	if report.OK() {
		t.Fatal("the zero value must be refused")
	}
	violation := report[0]
	if violation.Path != "name" || violation.Rule != "required" {
		t.Errorf("violation = %+v, want path=name rule=required", violation)
	}
	if violation.Code != svcvalidation.CodeRequired {
		t.Errorf("code = %v, want CodeRequired", violation.Code)
	}
	//: an explicit zero is indistinguishable from an absent one; the doc says
	//: so and the behaviour must match the doc.
	if report := svcvalidation.Required[int]()("n", 0); report.OK() {
		t.Error("an explicit zero is refused too — that is the documented limitation")
	}
}

// TestBoundsAcceptTheirOwnLimits pins the bounds as CLOSED intervals: a rule
// written "min=8" that refused 8 would be a rule nobody could satisfy at the
// edge, and edges are where the bug reports come from.
func TestBoundsAcceptTheirOwnLimits(t *testing.T) {
	t.Parallel()
	if report := svcvalidation.AtLeast(8)("n", 8); !report.OK() {
		t.Error("Min is inclusive")
	}
	if report := svcvalidation.AtMost(8)("n", 8); !report.OK() {
		t.Error("Max is inclusive")
	}
	between := svcvalidation.Must(svcvalidation.Between(1, 10))
	for _, value := range []int{1, 5, 10} {
		if report := between("n", value); !report.OK() {
			t.Errorf("Between(1,10) must accept %d", value)
		}
	}
	for _, value := range []int{0, 11} {
		if report := between("n", value); report.OK() {
			t.Errorf("Between(1,10) must refuse %d", value)
		}
	}
	//: an interval of exactly one value is a coherent requirement.
	single := svcvalidation.Must(svcvalidation.Between(3, 3))
	if report := single("n", 3); !report.OK() {
		t.Error("Between(3,3) must accept 3 — a one-value interval is legitimate")
	}
}

// TestLengthCountsRunesNotBytes is the difference between "at most 30
// characters" and a rule that refuses an ordinary accented name at 16.
func TestLengthCountsRunesNotBytes(t *testing.T) {
	t.Parallel()
	constraint := svcvalidation.Must(svcvalidation.Length(1, 4))
	//: 4 runes, 8 bytes.
	if report := constraint("name", "éàüö"); !report.OK() {
		t.Errorf("Length must count runes, not bytes: %v", report)
	}
	if report := constraint("name", "abcde"); report.OK() {
		t.Error("5 runes must be refused by Length(1,4)")
	}
	//: the open-ended form must not read as "8 to -1".
	unbounded := svcvalidation.Must(svcvalidation.Length(8, svcvalidation.Unbounded))
	if report := unbounded("name", strings.Repeat("a", 1000)); !report.OK() {
		t.Error("Unbounded must impose no ceiling")
	}
	report := unbounded("name", "short")
	if report.OK() {
		t.Fatal("Length(8, Unbounded) must still impose its floor")
	}
	if !strings.Contains(report[0].Message, "or more") {
		t.Errorf("open-ended message = %q, want it to read as open-ended", report[0].Message)
	}
}

// TestCountIsHowASliceExpressesPresence records the reason Required is
// comparable-only: a slice is not, and its presence question is its length.
func TestCountIsHowASliceExpressesPresence(t *testing.T) {
	t.Parallel()
	constraint := svcvalidation.Must(svcvalidation.Count[string](1, svcvalidation.Unbounded))
	if report := constraint("tags", nil); report.OK() {
		t.Error("a nil slice holds nothing and must fail Count(1, Unbounded)")
	}
	if report := constraint("tags", []string{}); report.OK() {
		t.Error("an empty slice holds nothing either")
	}
	if report := constraint("tags", []string{"a"}); !report.OK() {
		t.Error("one element satisfies Count(1, Unbounded)")
	}
}

// TestOneOfEnumeratesTheSchemaAndNotTheValue pins both halves: the message is
// actionable because it lists the schema, and safe because it lists only that.
func TestOneOfEnumeratesTheSchemaAndNotTheValue(t *testing.T) {
	t.Parallel()
	constraint := svcvalidation.Must(svcvalidation.OneOf("red", "green", "blue"))
	if report := constraint("colour", "green"); !report.OK() {
		t.Error("a member of the set must be accepted")
	}
	report := constraint("colour", secretValue)
	if report.OK() {
		t.Fatal("a value outside the set must be refused")
	}
	if !strings.Contains(report[0].Message, "red, green, blue") {
		t.Errorf("message = %q, want it to enumerate the allowed values", report[0].Message)
	}
	if strings.Contains(report[0].Message, secretValue) {
		t.Errorf("message leaked the rejected value: %q", report[0].Message)
	}
}

// TestMatchesCompilesAtConstruction proves the pattern never reaches a request
// uncompiled, and that a bad one is a construction refusal rather than a panic
// at the first match.
func TestMatchesCompilesAtConstruction(t *testing.T) {
	t.Parallel()
	constraint := svcvalidation.Must(svcvalidation.Matches(`^[a-z]{2}-[0-9]{4}$`))
	if report := constraint("ref", "ab-1234"); !report.OK() {
		t.Errorf("a matching value must be accepted: %v", report)
	}
	report := constraint("ref", "nope")
	if report.OK() {
		t.Fatal("a non-matching value must be refused")
	}
	if report[0].Code != svcvalidation.CodePatternMismatch {
		t.Errorf("code = %v, want CodePatternMismatch", report[0].Code)
	}
	if _, err := svcvalidation.Matches("[unclosed"); err == nil {
		t.Fatal("an uncompilable pattern must be refused at construction")
	}
}

// TestConstructorsRefuseWhatCanNeverBeSatisfied is ADR 0031's refuse half, on
// every constructor that has a way to be misconfigured. A constraint nothing
// can satisfy is not a strict rule, it is a broken one, and it must not become
// a validator that rejects everything while looking like it works.
func TestConstructorsRefuseWhatCanNeverBeSatisfied(t *testing.T) {
	t.Parallel()
	build := map[string]func() error{
		"Between with an inverted interval": func() error {
			_, err := svcvalidation.Between(10, 1)
			return err
		},
		"Length with a negative minimum": func() error {
			_, err := svcvalidation.Length(-1, 10)
			return err
		},
		"Length with an inverted interval": func() error {
			_, err := svcvalidation.Length(10, 3)
			return err
		},
		"Count with an inverted interval": func() error {
			_, err := svcvalidation.Count[int](10, 3)
			return err
		},
		"OneOf with an empty set": func() error {
			_, err := svcvalidation.OneOf[string]()
			return err
		},
		"Matches with an uncompilable pattern": func() error {
			_, err := svcvalidation.Matches("(")
			return err
		},
		"Field with a nil accessor": func() error {
			_, err := svcvalidation.Field[int, int]("n", nil)
			return err
		},
		"Field with an empty name": func() error {
			_, err := svcvalidation.Field("", func(v int) int { return v })
			return err
		},
		"Each with a nil accessor": func() error {
			_, err := svcvalidation.Each[int, int]("n", nil)
			return err
		},
	}
	for name, run := range build {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := run()
			if err == nil {
				t.Fatal("must be refused at construction, not carried into a validation")
			}
			if !errs.HasCode(err, corevalidation.CodeConstraintMisconfigured) {
				t.Errorf("err = %v, want CONSTRAINT_MISCONFIGURED", err)
			}
		})
	}
}

// TestNoMessageEchoesTheValue is the executable form of the security property
// in ViolationValue.Message. It runs every built-in constraint against a value
// that must never appear in a message and asserts none of them repeats it.
func TestNoMessageEchoesTheValue(t *testing.T) {
	t.Parallel()
	//: required is absent from this table on purpose: its message is a
	//: compile-time literal that cannot contain anything, and no non-empty
	//: probe value can make it refuse.
	constraints := map[string]corevalidation.Constraint[string]{
		"min":     svcvalidation.AtLeast("zzzz"),
		"max":     svcvalidation.AtMost("!"),
		"between": svcvalidation.Must(svcvalidation.Between("!", "\"")),
		"length":  svcvalidation.Must(svcvalidation.Length(1, 3)),
		"one_of":  svcvalidation.Must(svcvalidation.OneOf("red")),
		"pattern": svcvalidation.Must(svcvalidation.Matches("^never$")),
	}
	for name, constraint := range constraints {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			report := constraint("secret", secretValue)
			if report.OK() {
				t.Fatalf("%s was expected to refuse the probe value", name)
			}
			for _, violation := range report {
				if strings.Contains(violation.Message, secretValue) {
					t.Errorf("%s leaked the rejected value into its message: %q", name, violation.Message)
				}
			}
		})
	}
}
