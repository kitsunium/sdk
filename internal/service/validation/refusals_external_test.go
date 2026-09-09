package validation_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcvalidation "github.com/kitsunium/sdk/internal/service/validation"
)

// Each type below carries exactly one tag the dialect refuses. They exist so
// the refusal is proved to fire, and proved to NAME the construct: "invalid
// tag" would leave the caller unsure whether they mistyped or asked for
// something this dialect does not have.
type (
	withEmail struct {
		Address string `validate:"email"`
	}
	withURL struct {
		Link string `validate:"url"`
	}
	withUUID struct {
		Ref string `validate:"uuid"`
	}
	withPattern struct {
		Code string `validate:"pattern=^a$"`
	}
	withRegex struct {
		Code string `validate:"regex=^a$"`
	}
	withUnknown struct {
		Value string `validate:"definitelyNotARule"`
	}
	withMinOnString struct {
		Name string `validate:"min=3"`
	}
	withMinOnSlice struct {
		Tags []string `validate:"min=3"`
	}
	withMinLenOnInt struct {
		Count int `validate:"minlen=3"`
	}
	withMinCountOnString struct {
		Name string `validate:"mincount=1"`
	}
	withDiveOnMap struct {
		Limits map[string]int `validate:"dive"`
	}
	withDiveOnScalar struct {
		Count int `validate:"dive"`
	}
	withDiveTailOnStruct struct {
		Home address `validate:"dive,required"`
	}
	withTwoDives struct {
		Rows []string `validate:"dive,dive"`
	}
	withOneOfOnFloat struct {
		Ratio float64 `validate:"oneof=1.5|2.5"`
	}
	withOneOfEmpty struct {
		Colour string `validate:"oneof="`
	}
	withOneOfBadInt struct {
		Level int `validate:"oneof=1|x"`
	}
	withBadNumericArg struct {
		Count int `validate:"min=abc"`
	}
	withMissingArg struct {
		Count int `validate:"min"`
	}
	withRequiredArg struct {
		Count int `validate:"required=true"`
	}
	withNegativeSize struct {
		Name string `validate:"minlen=-1"`
	}
)

// TestTheDialectRefusesByName is the deliverable's other half: every construct
// the SDK declines to support is declined explicitly, at COMPILE time, with a
// message that says what to do instead.
func TestTheDialectRefusesByName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		compile func() error
		wants   string
	}{
		{"email", compileOf[withEmail], "confirmation"},
		{"url", compileOf[withURL], "net/url"},
		{"uuid", compileOf[withUUID], "pkg/v1/id"},
		{"pattern", compileOf[withPattern], "validation.Matches"},
		{"regex", compileOf[withRegex], "validation.Matches"},
		{"an unknown rule", compileOf[withUnknown], "unknown rule"},
		{"min on a string", compileOf[withMinOnString], "minlen"},
		{"min on a slice", compileOf[withMinOnSlice], "mincount"},
		{"minlen on an int", compileOf[withMinLenOnInt], "mincount"},
		{"mincount on a string", compileOf[withMinCountOnString], "minlen"},
		{"dive into a map", compileOf[withDiveOnMap], "map"},
		{"dive into a scalar", compileOf[withDiveOnScalar], "struct, a slice or an array"},
		{"element rules after diving a struct", compileOf[withDiveTailOnStruct], "nested type"},
		{"two dives on one field", compileOf[withTwoDives], "only one dive"},
		{"oneof on a float", compileOf[withOneOfOnFloat], "string or integer"},
		{"oneof with an empty list", compileOf[withOneOfEmpty], "separated list"},
		{"oneof with a non-numeric entry", compileOf[withOneOfBadInt], "whole number"},
		{"a non-numeric bound", compileOf[withBadNumericArg], "whole number"},
		{"a bound with no argument", compileOf[withMissingArg], "numeric argument"},
		{"required with an argument", compileOf[withRequiredArg], "no argument"},
		{"a negative size", compileOf[withNegativeSize], "negative"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.compile()
			if err == nil {
				t.Fatal("must be refused at compile time, not accepted and quietly ignored")
			}
			if !errs.HasCode(err, svcvalidation.CodeInvalidRule) {
				t.Fatalf("err = %v, want INVALID_RULE", err)
			}
			//: the refusal must NAME the fix; the field carrying it is what a
			//: developer reads first.
			fields := renderFields(err)
			if !strings.Contains(fields["clause"], tc.wants) {
				t.Errorf("clause = %q, want it to mention %q", fields["clause"], tc.wants)
			}
		})
	}
}

// compileOf adapts Struct[T] to the table's func() error shape.
func compileOf[T any]() error {
	_, err := svcvalidation.Struct[T](svcvalidation.StructConfig{})
	return err
}

// renderFields flattens an error's structured fields for assertion.
func renderFields(err error) map[string]string {
	fields := map[string]string{}
	for _, field := range errs.FieldsOf(err) {
		fields[field.Key()] = field.StringValue()
	}
	return fields
}

// TestARefusalNamesTheField proves the diagnosis points at a place in the
// source: a refusal that said only "bad tag" on a 40-field struct would be a
// search, not an error.
func TestARefusalNamesTheField(t *testing.T) {
	t.Parallel()
	err := compileOf[withMinOnString]()
	if err == nil {
		t.Fatal("expected a refusal")
	}
	fields := renderFields(err)
	if fields["field"] != "Name" {
		t.Errorf("field = %q, want the offending member name", fields["field"])
	}
	if fields["rule"] != "min" {
		t.Errorf("rule = %q, want the offending rule", fields["rule"])
	}
}
