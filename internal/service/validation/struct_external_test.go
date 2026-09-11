package validation_test

import (
	"slices"
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

// item is one element of a dived collection.
type item struct {
	SKU string `json:"sku" validate:"required"`
}

// withSlicePointer and withArrayPointer dive through a pointer to a
// collection, the shape an optional list takes in a decoded document.
type withSlicePointer struct {
	Items *[]item `json:"items" validate:"dive"`
}

type withArrayPointer struct {
	Items *[3]item `json:"items" validate:"dive"`
}

// withRequiredElements asks for every element to be PRESENT;
// withOptionalElements dives the same slice without asking.
type withRequiredElements struct {
	Items []*item `json:"items" validate:"dive,required"`
}

type withOptionalElements struct {
	Items []*item `json:"items" validate:"dive"`
}

// withRequiredCounts puts presence on pointers to a type whose zero value is a
// legitimate reading, which is the reason to declare a pointer at all.
type withRequiredCounts struct {
	Counts []*int `json:"counts" validate:"dive,required"`
}

// Common is embedded by the promotion fixtures below. encoding/json lifts the
// members of an embedded struct into the object that embeds it, so "zip" is a
// key of that object and "Common" is a key of nothing.
type Common struct {
	Zip string `json:"zip" validate:"required"`
}

type withEmbedded struct {
	Common `validate:"dive"`
}

type withEmbeddedPointer struct {
	*Common `validate:"dive"`
}

// withOptionOnlyEmbedding names no key either: an empty json name is no name,
// and JSON promotes it exactly as it promotes an untagged one.
type withOptionOnlyEmbedding struct {
	Common `json:",omitzero" validate:"dive"`
}

// withNamedEmbedding gives the embedding a json name, which makes it an
// ordinary member with a key of its own.
type withNamedEmbedding struct {
	Common `json:"common" validate:"dive"`
}

// Label is not a struct, and JSON keys an embedded non-struct by its type name.
type Label string

type withEmbeddedLabel struct {
	Label `validate:"required"`
}

// The width fixtures sit at the edges of their field's own range.
type (
	withFullInt8Range struct {
		Level int8 `json:"level" validate:"min=-128,max=127"`
	}
	withFloat32Ceiling struct {
		Ratio float32 `json:"ratio" validate:"max=0.1"`
	}
	withUint8Set struct {
		Level uint8 `json:"level" validate:"oneof=0|255"`
	}
)

// mustStruct compiles a validator the case expects to compile.
func mustStruct[T any](tb testing.TB, cfg svcvalidation.StructConfig) corevalidation.Constraint[T] {
	tb.Helper()
	constraint, err := svcvalidation.Struct[T](cfg)
	if err != nil {
		tb.Fatalf("Struct refused a legal type: %v", err)
	}
	return constraint
}

// pathsOf validates value from the root with the tags of T and returns where
// each violation is, in report order.
func pathsOf[T any](tb testing.TB, cfg svcvalidation.StructConfig, value T) []string {
	tb.Helper()
	return mustStruct[T](tb, cfg)(corevalidation.RootPath, value).Paths()
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

// TestAPromotedEmbeddingAddsNoPathSegment follows the json name one step
// further, to the one member JSON does not key at all. An embedded struct with
// no json name is PROMOTED: its members become members of the object that
// embeds it, so the key an operator writes is "zip" and never "Common.zip".
// config.Load decodes every format through that JSON round trip, so a path
// naming the embedding points at something no input contains.
//
// The rule is encoding/json's own, and each row mirrors what json.Marshal
// produces for the same type: promoted when the embedded type is a struct, or
// a pointer to one, and its json name is empty. A json name makes it an
// ordinary member with a key; an embedded non-struct is keyed by its type name.
//
// MUTATION-CHECKED, three ways. Naming every embedding by its Go name, as
// shipped, fails the three promoted rows with
// `paths = [Common.zip], want [zip]`, as the code before the fix did.
// Promoting an embedding whose json tag names it fails the named row with
// `paths = [zip], want [common.zip]`. Promoting every anonymous field without
// a json name, struct or not, fails the Label row with
// `paths = [], want [Label]` — a promoted scalar is located at its parent,
// here the root.
func TestAPromotedEmbeddingAddsNoPathSegment(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		paths func(tb testing.TB) []string
		want  []string
	}
	collect := svcvalidation.StructConfig{}
	tests := []tc{
		{
			name: "an untagged embedded struct",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withEmbedded{})
			},
			want: []string{"zip"},
		},
		{
			name: "an untagged embedded pointer to a struct",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withEmbeddedPointer{Common: &Common{}})
			},
			want: []string{"zip"},
		},
		{
			name: "an embedding whose json tag carries options and no name",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withOptionOnlyEmbedding{})
			},
			want: []string{"zip"},
		},
		{
			name: "an embedding with a json name keeps its segment",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withNamedEmbedding{})
			},
			want: []string{"common.zip"},
		},
		{
			name: "an embedded non-struct is keyed by its type name",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withEmbeddedLabel{})
			},
			want: []string{"Label"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.paths(t); !slices.Equal(got, c.want) {
			t.Errorf("paths = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestTagBoundsAreParsedAtTheFieldsWidth is the accepting half of the width
// refusals in TestTheDialectRefusesByName: a literal is read as a value of the
// FIELD's type, so a bound at the very edge of the field's range compiles and
// holds, and an inclusive float32 ceiling admits the float32 its own literal
// spells. Read at 64 bits, max=0.1 is the double nearest 0.1, which the
// float32 nearest 0.1 (0.100000001…) exceeds — the field set to exactly the
// value in its tag would be refused.
//
// MUTATION-CHECKED: parsing the float bound at 64 bits again fails the
// float32 row with `paths = [ratio], want []`, as the code before the fix did.
func TestTagBoundsAreParsedAtTheFieldsWidth(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		paths func(tb testing.TB) []string
		want  []string
	}
	collect := svcvalidation.StructConfig{}
	tests := []tc{
		{
			name: "the lowest int8 meets min=-128",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withFullInt8Range{Level: -128})
			},
		},
		{
			name: "the highest int8 meets max=127",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withFullInt8Range{Level: 127})
			},
		},
		{
			name: "a float32 set to its ceiling's own literal is within it",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withFloat32Ceiling{Ratio: 0.1})
			},
		},
		{
			name: "a float32 above its ceiling is not",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withFloat32Ceiling{Ratio: 0.2})
			},
			want: []string{"ratio"},
		},
		{
			name: "the highest uint8 is a member of a set that names it",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withUint8Set{Level: 255})
			},
		},
		{
			name: "a uint8 the set does not name is refused",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withUint8Set{Level: 7})
			},
			want: []string{"level"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.paths(t); !slices.Equal(got, c.want) {
			t.Errorf("paths = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
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

// TestDiveThroughAPointerToACollection is TestDiveThroughAPointer for the
// other thing dive reaches: a pointer to a slice or an array follows the same
// one-level pointer rule a pointer to a struct does. An absent collection
// contributes nothing; a present one is walked element by element, and its
// violations are located exactly as a bare slice's would be.
//
// MUTATION-CHECKED: elementsStep reading the field without first following
// the pointer — which is how it shipped, the compiler having resolved the
// pointer and then dropped the fact — compiles every row, and every row then
// panics on its first validation, nil or not. The slice rows panic with
// `reflect: call of reflect.Value.Len on ptr to non-array Value`; the array
// rows get one call further, because reflect answers Len on a pointer to an
// array with the array's length, and panic with
// `reflect: call of reflect.Value.Index on ptr Value`. The code before the fix
// panicked identically. Each shape was run alone, since the first panic ends
// the test binary.
func TestDiveThroughAPointerToACollection(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		paths func(tb testing.TB) []string
		want  []string
	}
	collect := svcvalidation.StructConfig{}
	tests := []tc{
		{
			name: "a nil pointer to a slice holds nothing to be wrong about",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withSlicePointer{})
			},
		},
		{
			name: "a present slice is walked element by element",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withSlicePointer{Items: &[]item{{SKU: "a"}, {}}})
			},
			want: []string{"items[1].sku"},
		},
		{
			name: "a nil pointer to an array holds nothing to be wrong about",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withArrayPointer{})
			},
		},
		{
			name: "a present array is walked element by element",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withArrayPointer{Items: &[3]item{{SKU: "a"}, {}, {SKU: "c"}}})
			},
			want: []string{"items[1].sku"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.paths(t); !slices.Equal(got, c.want) {
			t.Errorf("paths = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDiveRequiredAsksAboutTheElementPointer pins what `required` means after
// dive when the elements are pointers: the ELEMENT is present, which for a
// pointer is "not nil" — the same question `required` asks of a pointer field,
// and the one the programmatic Each(…, Required[*T]()) asks. So a nil element
// is a violation located at the element itself, a non-nil pointer to a zero
// value is present (that is what declaring a pointer buys), and plain dive,
// which asks nothing, still passes over a nil element in silence.
//
// MUTATION-CHECKED, twice. Letting a nil element return before any rule runs,
// and asking presence of the value behind the pointer — exactly how it
// shipped — fails four rows: `paths = [], want [items[0]]` for the lone nil
// and again under stop-at-first,
// `paths = [items[2] items[2].sku], want [items[1] items[2].sku]` for the mixed
// slice, where the nil element vanished and the present zero one was reported
// absent, and `paths = [counts[1]], want [counts[0]]`, the same inversion on
// the counts. Asking presence of the pointer AND again of the value behind it
// fails the two rows with a present zero:
// `paths = [items[1] items[2] items[2].sku], want [items[1] items[2].sku]` and
// `paths = [counts[0] counts[1]], want [counts[0]]` — the value a caller
// declared a pointer to be able to send, refused as missing. The code before
// the fix failed the first four rows the same way.
func TestDiveRequiredAsksAboutTheElementPointer(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		paths func(tb testing.TB) []string
		want  []string
	}
	collect := svcvalidation.StructConfig{}
	tests := []tc{
		{
			name: "a nil element is absent",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withRequiredElements{Items: []*item{nil}})
			},
			want: []string{"items[0]"},
		},
		{
			name: "plain dive asks nothing of a nil element",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withOptionalElements{Items: []*item{nil}})
			},
		},
		{
			//: the absent element stops there; the present zero one is
			//: walked, and its own member rule fires.
			name: "absence and a present element's own rules are reported in index order",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withRequiredElements{Items: []*item{{SKU: "a"}, nil, {}}})
			},
			want: []string{"items[1]", "items[2].sku"},
		},
		{
			name: "stop-at-first stops at the first absent element",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, svcvalidation.StructConfig{StopAtFirst: true},
					withRequiredElements{Items: []*item{nil, nil}})
			},
			want: []string{"items[0]"},
		},
		{
			name: "a non-nil pointer to a zero value is present",
			paths: func(tb testing.TB) []string {
				return pathsOf(tb, collect, withRequiredCounts{Counts: []*int{nil, new(int)}})
			},
			want: []string{"counts[0]"},
		},
		{
			//: the programmatic spelling of the same rule answers the same.
			name: "the code path asks the same question",
			paths: func(testing.TB) []string {
				rules := svcvalidation.Must(svcvalidation.Each("counts",
					func(value withRequiredCounts) []*int { return value.Counts },
					svcvalidation.Required[*int]()))
				return rules(corevalidation.RootPath, withRequiredCounts{Counts: []*int{nil, new(int)}}).Paths()
			},
			want: []string{"counts[0]"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.paths(t); !slices.Equal(got, c.want) {
			t.Errorf("paths = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
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
