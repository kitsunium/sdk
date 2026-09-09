package validation_test

import (
	"strings"
	"sync"
	"testing"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcvalidation "github.com/kitsunium/sdk/internal/service/validation"
)

// untagged carries no validate tag at all — the ADR 0031 "no constraint is
// legitimate" case, expressed through the tag front end.
type untagged struct {
	Name string `json:"name"`
}

// renamed proves the path uses the json name rather than the Go name.
type renamed struct {
	PostalCode string `json:"zip" validate:"required"`
}

// unnamed has no json tag, so the Go name is the only name there is.
type unnamed struct {
	PostalCode string `validate:"required"`
}

// withPointer dives through a pointer; a nil one holds nothing to be wrong
// about.
type withPointer struct {
	Home *address `json:"home" validate:"dive"`
}

// recursiveNode reaches itself through dive and must be refused at compile.
type recursiveNode struct {
	Next *recursiveNode `json:"next" validate:"dive"`
}

// diamond reaches the same type twice without a cycle; it must COMPILE.
type diamond struct {
	Billing  address `json:"billing"  validate:"dive"`
	Shipping address `json:"shipping" validate:"dive"`
}

// mustStruct compiles a validator the case expects to compile.
func mustStruct[T any](tb testing.TB, cfg svcvalidation.StructConfig) corevalidation.Constraint[T] {
	tb.Helper()
	constraint, err := svcvalidation.Struct[T](cfg)
	if err != nil {
		tb.Fatalf("Struct refused a legal type: %v", err)
	}
	return constraint
}

// TestStructBuildsTheDocumentedPath is the tag path's headline claim: a
// violation three levels down names all three levels.
func TestStructBuildsTheDocumentedPath(t *testing.T) {
	t.Parallel()
	rules := mustStruct[user](t, svcvalidation.StructConfig{})
	value := user{
		Name:      "ada",
		Age:       36,
		Addresses: []address{{Zip: "75001"}, {Zip: "13001"}, {Zip: "x"}},
	}
	report := rules(corevalidation.RootPath, value)
	if len(report) != 1 {
		t.Fatalf("exactly one element is invalid, got %d: %v", len(report), report.Paths())
	}
	if report[0].Path != "addresses[2].zip" {
		t.Errorf("path = %q, want %q", report[0].Path, "addresses[2].zip")
	}
	if report[0].Rule != "length" || report[0].Code != svcvalidation.CodeLengthOutOfRange {
		t.Errorf("violation = %+v, want the length rule", report[0])
	}
}

// TestStructCollectsEveryViolationAcrossFields pins the default across a whole
// struct, and the declaration order that makes two reports diffable.
func TestStructCollectsEveryViolationAcrossFields(t *testing.T) {
	t.Parallel()
	rules := mustStruct[user](t, svcvalidation.StructConfig{})
	report := rules(corevalidation.RootPath, user{Age: 200})
	want := []string{"name", "age", "addresses"}
	if len(report) != len(want) {
		t.Fatalf("want %d violations, got %d: %v", len(want), len(report), report.Paths())
	}
	for index, path := range report.Paths() {
		if path != want[index] {
			t.Errorf("paths[%d] = %q, want %q (declaration order)", index, path, want[index])
		}
	}
}

// TestStopAtFirstReallyStops proves the mode is compiled into the plan rather
// than applied as a filter: exactly one violation, and it is the earliest.
func TestStopAtFirstReallyStops(t *testing.T) {
	t.Parallel()
	rules := mustStruct[user](t, svcvalidation.StructConfig{StopAtFirst: true})
	report := rules(corevalidation.RootPath, user{Age: 200})
	if len(report) != 1 {
		t.Fatalf("StopAtFirst must report exactly one violation, got %v", report.Paths())
	}
	if report[0].Path != "name" {
		t.Errorf("path = %q, want the earliest field %q", report[0].Path, "name")
	}
	//: and inside a dived slice, the first invalid ELEMENT wins.
	deep := mustStruct[user](t, svcvalidation.StructConfig{StopAtFirst: true})
	report = deep(corevalidation.RootPath, user{
		Name: "ada", Addresses: []address{{Zip: "75001"}, {Zip: "x"}, {Zip: "y"}},
	})
	if len(report) != 1 || report[0].Path != "addresses[1].zip" {
		t.Errorf("report = %v, want exactly addresses[1].zip", report.Paths())
	}
}

// TestATypeWithNoValidateTagCompilesAndPasses is the ADR 0031 trap, stated at
// the type level: "no rules" must be a rule set, or validation cannot be
// adopted one field at a time.
func TestATypeWithNoValidateTagCompilesAndPasses(t *testing.T) {
	t.Parallel()
	rules := mustStruct[untagged](t, svcvalidation.StructConfig{})
	if report := rules(corevalidation.RootPath, untagged{}); !report.OK() {
		t.Errorf("an untagged type must accept everything, got %v", report)
	}
}

// TestPathUsesTheJSONNameWhenThereIsOne records the reason: config.Load
// decodes every format through a JSON round trip, so the json name is the key
// the operator actually wrote.
func TestPathUsesTheJSONNameWhenThereIsOne(t *testing.T) {
	t.Parallel()
	tagged := mustStruct[renamed](t, svcvalidation.StructConfig{})
	report := tagged(corevalidation.RootPath, renamed{})
	if len(report) != 1 || report[0].Path != "zip" {
		t.Errorf("path = %v, want the json name %q", report.Paths(), "zip")
	}
	plain := mustStruct[unnamed](t, svcvalidation.StructConfig{})
	report = plain(corevalidation.RootPath, unnamed{})
	if len(report) != 1 || report[0].Path != "PostalCode" {
		t.Errorf("path = %v, want the Go name %q", report.Paths(), "PostalCode")
	}
}

// TestDiveThroughAPointer pins both halves: a present pointer is walked, an
// absent one contributes nothing. Requiring it to be present is `required`, a
// different question asked at the field.
func TestDiveThroughAPointer(t *testing.T) {
	t.Parallel()
	rules := mustStruct[withPointer](t, svcvalidation.StructConfig{})
	if report := rules(corevalidation.RootPath, withPointer{}); !report.OK() {
		t.Errorf("a nil pointer holds nothing to be wrong about, got %v", report)
	}
	report := rules(corevalidation.RootPath, withPointer{Home: &address{Zip: "x"}})
	if len(report) != 1 || report[0].Path != "home.zip" {
		t.Errorf("report = %v, want home.zip", report.Paths())
	}
}

// TestADiamondCompilesButACycleIsRefused separates the two: two fields of the
// same type are ordinary, a type that reaches itself has no compile-time depth.
func TestADiamondCompilesButACycleIsRefused(t *testing.T) {
	t.Parallel()
	rules := mustStruct[diamond](t, svcvalidation.StructConfig{})
	report := rules(corevalidation.RootPath, diamond{})
	//: both branches are walked, and an absent Zip trips BOTH of its rules —
	//: required and minlen are peers, not a gate and a follower. Collect-all
	//: means the report describes what a valid value looks like, not only the
	//: first thing that is wrong with this one.
	want := []string{"billing.zip", "billing.zip", "shipping.zip", "shipping.zip"}
	if len(report) != len(want) {
		t.Fatalf("want %d violations, got %v", len(want), report.Paths())
	}
	for index, path := range report.Paths() {
		if path != want[index] {
			t.Errorf("paths[%d] = %q, want %q", index, path, want[index])
		}
	}
	_, err := svcvalidation.Struct[recursiveNode](svcvalidation.StructConfig{})
	if err == nil {
		t.Fatal("a recursive type must be refused at compile time")
	}
	if !errs.HasCode(err, svcvalidation.CodeInvalidRule) {
		t.Errorf("err = %v, want INVALID_RULE", err)
	}
}

// TestStructRefusesANonStructTarget covers the other compile-time refusal.
func TestStructRefusesANonStructTarget(t *testing.T) {
	t.Parallel()
	_, err := svcvalidation.Struct[string](svcvalidation.StructConfig{})
	if err == nil {
		t.Fatal("a non-struct T must be refused")
	}
	if !errs.HasCode(err, svcvalidation.CodeUnsupportedTarget) {
		t.Errorf("err = %v, want UNSUPPORTED_TARGET", err)
	}
	if !strings.Contains(err.Error(), "struct") {
		t.Errorf("err = %q, want it to name what is required", err.Error())
	}
}

// TestThePlanCacheIsSafeAndStable exercises the concurrent-compile path under
// -race and proves a second Struct call behaves identically to the first.
func TestThePlanCacheIsSafeAndStable(t *testing.T) {
	t.Parallel()
	const goroutines int = 16
	var wait sync.WaitGroup
	results := make([]corevalidation.ReportValue, goroutines)
	//: every goroutine compiles (or fetches) the same plan concurrently.
	for index := range goroutines {
		wait.Go(func() {
			constraint, err := svcvalidation.Struct[user](svcvalidation.StructConfig{})
			if err != nil {
				return
			}
			results[index] = constraint(corevalidation.RootPath, user{Age: 200})
		})
	}
	wait.Wait()
	//: every goroutine must have seen the same plan and the same report.
	for index, report := range results {
		if len(report) != 3 {
			t.Fatalf("goroutine %d saw %d violations, want 3: %v", index, len(report), report.Paths())
		}
	}
}
