package validation

import (
	"reflect"
	"testing"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
)

// benchAddress and benchUser are the shapes every benchmark below measures.
// They are deliberately ordinary: three scalar rules and a dived slice is what
// a request DTO looks like, and a benchmark on a one-field struct would only
// measure the call overhead.
type benchAddress struct {
	Zip string `json:"zip" validate:"required,minlen=4,maxlen=10"`
}

type benchUser struct {
	Name      string         `json:"name"      validate:"required,maxlen=64"`
	Age       int            `json:"age"       validate:"min=0,max=130"`
	Addresses []benchAddress `json:"addresses" validate:"mincount=1,dive"`
}

// benchValid is the value every "accepting" benchmark validates. The accepting
// path is the one that runs on every valid request, which is the overwhelming
// majority of them — it is the number that matters.
var benchValid = benchUser{
	Name:      "ada lovelace",
	Age:       36,
	Addresses: []benchAddress{{Zip: "75001"}, {Zip: "13001"}, {Zip: "69001"}},
}

// benchInvalid trips exactly one rule, three levels down.
var benchInvalid = benchUser{
	Name:      "ada lovelace",
	Age:       36,
	Addresses: []benchAddress{{Zip: "75001"}, {Zip: "13001"}, {Zip: "x"}},
}

// programmaticRules is the same rule set expressed WITHOUT reflection: an
// accessor per descent, resolved at compile time by the Go compiler.
func programmaticRules(tb testing.TB) corevalidation.Constraint[benchUser] {
	tb.Helper()
	zip, err := Field("zip", func(a benchAddress) string { return a.Zip },
		Required[string](), Must(Length(4, 10)))
	if err != nil {
		tb.Fatalf("build zip: %v", err)
	}
	addresses, err := Each("addresses", func(u benchUser) []benchAddress { return u.Addresses }, zip)
	if err != nil {
		tb.Fatalf("build addresses: %v", err)
	}
	name, err := Field("name", func(u benchUser) string { return u.Name },
		Required[string](), Must(Length(0, 64)))
	if err != nil {
		tb.Fatalf("build name: %v", err)
	}
	age, err := Field("age", func(u benchUser) int { return u.Age }, Must(Between(0, 130)))
	if err != nil {
		tb.Fatalf("build age: %v", err)
	}
	count, err := Field("addresses", func(u benchUser) []benchAddress { return u.Addresses },
		Must(Count[benchAddress](1, Unbounded)))
	if err != nil {
		tb.Fatalf("build count: %v", err)
	}
	return All(name, age, count, addresses)
}

// BenchmarkProgrammaticValid measures the reflection-free path on a valid
// value: the number the "the programmatic path stays usable without
// reflection" claim stands on.
func BenchmarkProgrammaticValid(b *testing.B) {
	rules := programmaticRules(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if report := rules(corevalidation.RootPath, benchValid); !report.OK() {
			b.Fatalf("the fixture must be valid, got %v", report)
		}
	}
}

// BenchmarkProgrammaticInvalid measures the same path when one element is
// wrong, so the cost of BUILDING a report is visible next to the cost of not
// needing one.
func BenchmarkProgrammaticInvalid(b *testing.B) {
	rules := programmaticRules(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if report := rules(corevalidation.RootPath, benchInvalid); report.OK() {
			b.Fatal("the fixture must be invalid")
		}
	}
}

// BenchmarkTagValid measures the struct-tag path on a valid value, against a
// plan that is already compiled. This is the number the cache exists for.
func BenchmarkTagValid(b *testing.B) {
	rules, err := Struct[benchUser](StructConfig{})
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if report := rules(corevalidation.RootPath, benchValid); !report.OK() {
			b.Fatalf("the fixture must be valid, got %v", report)
		}
	}
}

// BenchmarkTagInvalid measures the tag path when one element three levels down
// is wrong.
func BenchmarkTagInvalid(b *testing.B) {
	rules, err := Struct[benchUser](StructConfig{})
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if report := rules(corevalidation.RootPath, benchInvalid); report.OK() {
			b.Fatal("the fixture must be invalid")
		}
	}
}

// BenchmarkStructLookup measures what a caller pays for asking Struct[T] again
// — the shape a config.Validator's Validate method uses on every call. It must
// be a map read, not a compilation.
func BenchmarkStructLookup(b *testing.B) {
	//: warm the cache so the benchmark measures the steady state.
	if _, err := Struct[benchUser](StructConfig{}); err != nil {
		b.Fatalf("compile: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := Struct[benchUser](StructConfig{}); err != nil {
			b.Fatalf("lookup: %v", err)
		}
	}
}

// BenchmarkPlanCompile measures the work the cache removes from the hot path:
// one full walk of the type, its tags and its nested types. It is the number
// that says whether caching the plan was worth the sync.Map.
func BenchmarkPlanCompile(b *testing.B) {
	typ := reflect.TypeFor[benchUser]()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := compileStruct(typ, false, map[reflect.Type]bool{}); err != nil {
			b.Fatalf("compile: %v", err)
		}
	}
}
