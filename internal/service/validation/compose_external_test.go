package validation_test

import (
	"strings"
	"sync/atomic"
	"testing"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
	svcvalidation "github.com/kitsunium/sdk/internal/service/validation"
)

// address and user are the shapes the documented path example is built from.
type address struct {
	Zip string `json:"zip" validate:"required,minlen=4,maxlen=10"`
}

type user struct {
	Name      string    `json:"name"      validate:"required,maxlen=64"`
	Age       int       `json:"age"       validate:"min=0,max=130"`
	Addresses []address `json:"addresses" validate:"mincount=1,dive"`
}

// TestValidatorWithoutConstraintsPasses is the ADR 0031 half that is easy to
// get backwards. An empty rule set is not a broken validator — refusing it
// would make it impossible to adopt validation one field at a time.
func TestValidatorWithoutConstraintsPasses(t *testing.T) {
	t.Parallel()
	if report := svcvalidation.All[string]()("anything", "value"); !report.OK() {
		t.Errorf("All() with no constraint must accept, got %v", report)
	}
	if report := svcvalidation.First[string]()("anything", "value"); !report.OK() {
		t.Errorf("First() with no constraint must accept, got %v", report)
	}
	if err := svcvalidation.Check("value"); err != nil {
		t.Errorf("Check with no constraint must return nil, got %v", err)
	}
}

// TestAllCollectsEveryViolation pins the default. A form that reports one
// error at a time makes the user submit it five times.
func TestAllCollectsEveryViolation(t *testing.T) {
	t.Parallel()
	constraint := svcvalidation.All(
		svcvalidation.Must(svcvalidation.Length(10, svcvalidation.Unbounded)),
		svcvalidation.Must(svcvalidation.OneOf("red", "green")),
		svcvalidation.Must(svcvalidation.Matches("^[0-9]+$")),
	)
	report := constraint("colour", "blue")
	if len(report) != 3 {
		t.Fatalf("All must report every failure, got %d: %v", len(report), report.Paths())
	}
	//: composition order is report order, so two runs are diffable.
	wantRules := []string{"length", "one_of", "pattern"}
	for index, want := range wantRules {
		if report[index].Rule != want {
			t.Errorf("report[%d].Rule = %q, want %q", index, report[index].Rule, want)
		}
	}
}

// TestFirstReallyStops proves the short-circuit is real rather than a report
// truncated after the fact — the reason it is a combinator and not a flag.
func TestFirstReallyStops(t *testing.T) {
	t.Parallel()
	var ran atomic.Int64
	counting := func(string, string) corevalidation.ReportValue {
		ran.Add(1)
		return nil
	}
	refusing := svcvalidation.Required[string]()
	constraint := svcvalidation.First(refusing, counting, counting)
	report := constraint("name", "")
	if len(report) != 1 {
		t.Fatalf("First must report exactly the violation it stopped on, got %v", report)
	}
	if ran.Load() != 0 {
		t.Errorf("First evaluated %d constraints after the failure; it must evaluate none", ran.Load())
	}
	//: and when nothing refuses, everything runs.
	ran.Store(0)
	if report := svcvalidation.First(counting, counting)("n", "v"); !report.OK() {
		t.Fatalf("First must accept when no constraint refuses, got %v", report)
	}
	if ran.Load() != 2 {
		t.Errorf("First ran %d constraints, want 2 when none refuses", ran.Load())
	}
}

// TestProgrammaticPathBuildsTheDocumentedLocation is the whole reason this
// domain exists: a violation on a nested slice element must name the element.
// It also proves the programmatic path needs no reflection — every descent
// here is an accessor function.
func TestProgrammaticPathBuildsTheDocumentedLocation(t *testing.T) {
	t.Parallel()
	zip := svcvalidation.Must(svcvalidation.Field("zip",
		func(a address) string { return a.Zip },
		svcvalidation.Must(svcvalidation.Length(4, 10)),
	))
	addresses := svcvalidation.Must(svcvalidation.Each("addresses",
		func(u user) []address { return u.Addresses }, zip,
	))
	rules := svcvalidation.Must(svcvalidation.Field("user", func(u user) user { return u }, addresses))

	value := user{Addresses: []address{{Zip: "75001"}, {Zip: "13001"}, {Zip: "x"}}}
	report := rules(corevalidation.RootPath, value)
	if len(report) != 1 {
		t.Fatalf("exactly one element is invalid, got %d: %v", len(report), report.Paths())
	}
	if report[0].Path != "user.addresses[2].zip" {
		t.Errorf("path = %q, want %q", report[0].Path, "user.addresses[2].zip")
	}
}

// TestEachVisitsEveryElement pins the collect-all default on a collection: an
// import that reports row 3 and stops makes the operator run it six times.
func TestEachVisitsEveryElement(t *testing.T) {
	t.Parallel()
	rules := svcvalidation.Must(svcvalidation.Each("rows",
		func(rows []string) []string { return rows },
		svcvalidation.Required[string](),
	))
	report := rules(corevalidation.RootPath, []string{"", "ok", "", ""})
	if len(report) != 3 {
		t.Fatalf("every invalid element must be reported, got %d: %v", len(report), report.Paths())
	}
	want := []string{"rows[0]", "rows[2]", "rows[3]"}
	for index, path := range report.Paths() {
		if path != want[index] {
			t.Errorf("paths[%d] = %q, want %q (index order)", index, path, want[index])
		}
	}
	//: a nil slice holds no element to be wrong about.
	if report := rules(corevalidation.RootPath, nil); !report.OK() {
		t.Errorf("a nil slice must contribute nothing, got %v", report)
	}
}

// TestCheckBridgesToTheErrorModel covers the shape a config.Validator needs.
func TestCheckBridgesToTheErrorModel(t *testing.T) {
	t.Parallel()
	rules := svcvalidation.Must(svcvalidation.Field("name",
		func(u user) string { return u.Name }, svcvalidation.Required[string]()))
	if err := svcvalidation.Check(user{Name: "ada"}, rules); err != nil {
		t.Fatalf("a valid value must produce a nil error, got %v", err)
	}
	err := svcvalidation.Check(user{}, rules)
	if err == nil {
		t.Fatal("an invalid value must produce an error")
	}
	if !strings.Contains(err.Error(), "VALIDATION_FAILED") {
		t.Errorf("err = %q, want the VALIDATION_FAILED reason", err.Error())
	}
}

// TestMustPanicsOnARefusedConstructor pins the regexp.MustCompile contract: a
// defect in a source literal stops the binary at init rather than being
// carried into a request.
func TestMustPanicsOnARefusedConstructor(t *testing.T) {
	t.Parallel()
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("Must must panic on a refused constructor")
		}
		message, ok := recovered.(string)
		if !ok || !strings.Contains(message, "CONSTRAINT_MISCONFIGURED") {
			t.Errorf("panic value = %v, want the typed error's own rendering", recovered)
		}
	}()
	//: an inverted interval; the value is never reached.
	svcvalidation.Must(svcvalidation.Between(10, 1))
}
