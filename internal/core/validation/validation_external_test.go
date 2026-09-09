package validation_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestPortIsAFunctionNotAnInterface is the executable form of ADR 0039's rule.
// A published interface cannot grow a method without breaking every downstream
// implementer at compile time; a func type cannot grow one at all. Turning
// Constraint into an interface fails here rather than in a consumer's build.
func TestPortIsAFunctionNotAnInterface(t *testing.T) {
	t.Parallel()
	if kind := reflect.TypeFor[corevalidation.Constraint[int]]().Kind(); kind != reflect.Func {
		t.Fatalf("Constraint must stay a func type (ADR 0039), got kind %v", kind)
	}
}

// TestPathGrammar pins the syntax a ViolationValue.Path speaks. It is the
// difference between a validator and an if statement, so it is asserted rather
// than described.
func TestPathGrammar(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"root field", corevalidation.JoinField(corevalidation.RootPath, "user"), "user"},
		{"nested field", corevalidation.JoinField("user", "address"), "user.address"},
		{"index", corevalidation.JoinIndex("addresses", 2), "addresses[2]"},
		{"index at root", corevalidation.JoinIndex(corevalidation.RootPath, 0), "[0]"},
		{
			"the documented example",
			corevalidation.JoinField(corevalidation.JoinIndex(
				corevalidation.JoinField("user", "addresses"), 2), "zip"),
			"user.addresses[2].zip",
		},
		//: an empty member name must not produce a trailing dot, which would
		//: read as a truncated path.
		{"empty member keeps the parent", corevalidation.JoinField("user", ""), "user"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Errorf("path = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

// TestEmptyReportIsPassingAndItsErrIsNil covers the half of ADR 0031 that is
// easiest to get wrong in Go: a report value that carried an error interface
// would make `if err != nil` true for a PASSING validation.
func TestEmptyReportIsPassingAndItsErrIsNil(t *testing.T) {
	t.Parallel()
	var report corevalidation.ReportValue
	if !report.OK() {
		t.Fatal("the zero ReportValue must be a passing report")
	}
	if _, ok := report.First(); ok {
		t.Error("a passing report has no first violation")
	}
	if paths := report.Paths(); paths != nil {
		t.Errorf("a passing report lists no paths, got %v", paths)
	}
	err := report.Err()
	//: the interface itself must be nil, not a typed nil wearing an interface.
	if err != nil {
		t.Fatalf("Err on a passing report must be nil, got %#v", err)
	}
}

// TestErrCarriesTheLocationsAndNotTheValues pins the error shape a failing
// report converts to: count and paths travel, messages and values do not.
func TestErrCarriesTheLocationsAndNotTheValues(t *testing.T) {
	t.Parallel()
	report := corevalidation.ReportValue{
		{Path: "user.name", Rule: "required", Message: "is required", Code: 0x00_03_2F_01},
		{Path: "user.addresses[2].zip", Rule: "length", Message: "must be between 4 and 10 characters long"},
	}
	err := report.Err()
	if err == nil {
		t.Fatal("a non-empty report must convert to an error")
	}
	if !errs.HasCode(err, corevalidation.CodeValidationFailed) {
		t.Errorf("Err must carry CodeValidationFailed, got %v", err)
	}
	if !errors.Is(err, corevalidation.ValidationFailed) {
		t.Error("Err must match the ValidationFailed sentinel")
	}
	fields := map[string]string{}
	for _, field := range errs.FieldsOf(err) {
		fields[field.Key()] = field.StringValue()
	}
	if fields["violations"] != "2" {
		t.Errorf("violations field = %q, want %q", fields["violations"], "2")
	}
	if !strings.Contains(fields["paths"], "user.addresses[2].zip") {
		t.Errorf("paths field = %q, want it to name every location", fields["paths"])
	}
	//: a message may be shown to an end user; it must not travel into a log
	//: line that an operator will paste into a ticket.
	if strings.Contains(err.Error(), "must be between") {
		t.Errorf("Err must not render a violation message, got %q", err.Error())
	}
}

// TestErrClipsALongPathList proves the diagnostic echo is bounded: 400 invalid
// fields must not produce a log line nobody can read.
func TestErrClipsALongPathList(t *testing.T) {
	t.Parallel()
	report := make(corevalidation.ReportValue, 0, 400)
	for index := range 400 {
		report = append(report, corevalidation.ViolationValue{
			Path: corevalidation.JoinIndex("rows", index), Rule: "required",
		})
	}
	fields := map[string]string{}
	for _, field := range errs.FieldsOf(report.Err()) {
		fields[field.Key()] = field.StringValue()
	}
	if !strings.HasSuffix(fields["paths"], "…") {
		t.Errorf("a long path list must be marked as clipped, got %q", fields["paths"])
	}
	if fields["violations"] != "400" {
		t.Errorf("the COUNT must stay exact even when the echo is clipped, got %q", fields["violations"])
	}
}

// TestPathsPreservesReportOrder pins the property that makes "collect
// everything" useful: two runs must produce the same list in the same order,
// or a diff of two reports is unreadable.
func TestPathsPreservesReportOrder(t *testing.T) {
	t.Parallel()
	report := corevalidation.ReportValue{
		{Path: "c"}, {Path: "a"}, {Path: "b"},
	}
	want := []string{"c", "a", "b"}
	got := report.Paths()
	if len(got) != len(want) {
		t.Fatalf("Paths length = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("Paths[%d] = %q, want %q (report order, never sorted)", index, got[index], want[index])
		}
	}
}

// TestSentinelsAreDistinctAndTyped guards the two core codes against a
// copy-paste that would make a misconfiguration indistinguishable from a
// failed validation — the exact confusion ADR 0046 §the ADR 0031 trap names.
func TestSentinelsAreDistinctAndTyped(t *testing.T) {
	t.Parallel()
	if corevalidation.CodeValidationFailed == corevalidation.CodeConstraintMisconfigured {
		t.Fatal("a failed validation and a broken constraint must not share a code")
	}
	if got, _ := errs.CodeOf(corevalidation.ConstraintMisconfigured); got != corevalidation.CodeConstraintMisconfigured {
		t.Errorf("ConstraintMisconfigured code = %v, want %v", got, corevalidation.CodeConstraintMisconfigured)
	}
	if status := errs.HTTPStatusOf(corevalidation.ValidationFailed); status != 422 {
		t.Errorf("ValidationFailed HTTP status = %d, want 422 — a rejected value is not a server fault", status)
	}
	if code := errs.ExitCodeOf(corevalidation.ConstraintMisconfigured); code != 78 {
		t.Errorf("ConstraintMisconfigured exit code = %d, want 78 (EX_CONFIG)", code)
	}
}
