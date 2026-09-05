// Package checks — the codec conformance checks.
package checks

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/e2e/harness"
	"github.com/kitsunium/sdk/pkg/v1/codec"
)

// wellFormedRow reports what is wrong with a Result, or "" when it is a row the
// RUNNER can tally.
//
// The conformance table has one row per check, so a Result with no name or no
// detail is a row nobody can act on without re-running the binary by hand. A
// status outside the four the runner knows falls into the skip bucket, quietly
// turning whatever happened into a non-event. And a row attributed to the wrong
// domain is worse than a missing one: it makes another domain look broken.
//
// It returns an error rather than taking a *testing.T so the failure is reported
// at the check under test rather than inside this helper.
func wellFormedRow(got harness.Result, domain string) error {
	//: the three ways a row can be unreadable, in the order they are noticed.
	switch {
	case got.Domain != domain:
		//: attributed to a domain it was not registered under.
		return fmt.Errorf("domain is %q, want %q: %+v", got.Domain, domain, got)
	case got.Name == "":
		//: a row with no check name cannot be matched to a behaviour.
		return fmt.Errorf("the Result carries no check name: %+v", got)
	case got.Detail == "":
		//: a row with no detail cannot be acted on.
		return fmt.Errorf("the Result carries no detail: %+v", got)
	}
	//: and the status has to be one the runner knows how to tally.
	switch got.Status {
	//: the four documented outcomes.
	case harness.Pass, harness.Fail, harness.Unsupported, harness.Skip:
		//: a row the runner can count.
		return nil
	//: anything else lands in the skip bucket and reports nothing.
	default:
		//: name the status so the drift is visible.
		return fmt.Errorf("status %q is tallied as a skip: %+v", got.Status, got)
	}
}

// passingRow is wellFormedRow plus the verdict a check with no environmental
// dependency must reach on any host that can run the test at all.
func passingRow(got harness.Result, domain string) error {
	//: an unreadable row is the first thing to report.
	if problem := wellFormedRow(got, domain); problem != nil {
		//: forward the row's own defect.
		return problem
	}
	//: pure computation has no environmental reason to reach anything else.
	if got.Status != harness.Pass {
		//: name the verdict and carry the detail that explains it.
		return fmt.Errorf("status is %v, want PASS: %s", got.Status, got.Detail)
	}
	//: a well-formed pass.
	return nil
}

// Test_codecRoundTrip pins that a round trip through a registered format is a
// PASS and that an unregistered one is not silently reported as one.
//
// The check is the whole point of the codec domain on a host: a format that
// marshals and then unmarshals to something else is exactly the failure a
// compile-only check cannot see.
func Test_codecRoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// format is the codec under test.
		format codec.Format
		// value is what is marshalled and read back.
		value any
		// wantStatus is the outcome the check must report.
		wantStatus harness.Status
	}
	tests := []tc{
		{name: "json", format: codec.JSON, value: map[string]any{"a": "b"}, wantStatus: harness.Pass},
		{
			//: a format nothing registered cannot round-trip, and the check has
			//: to say so rather than pass by default.
			name: "an unregistered format", format: codec.Format("nope"),
			value: map[string]any{"a": "b"}, wantStatus: harness.Fail,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := codecRoundTrip(c.format, c.value)

		if got.Status != c.wantStatus {
			t.Fatalf("Status = %v, want %v (detail %q)", got.Status, c.wantStatus, got.Detail)
		}
		//: every Result is diagnosable from the table alone.
		if got.Domain != codecDomain || got.Name == "" || got.Detail == "" {
			t.Fatalf("the Result is not diagnosable from the table: %+v", got)
		}
		//: the check names the format it exercised, or a failing row says
		//: nothing about which codec broke.
		if !strings.Contains(got.Name, string(c.format)) {
			t.Errorf("Name = %q, want it to name the format %q", got.Name, c.format)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_codecRegistryPopulated pins that the registry is not EMPTY on the host.
//
// An empty registry is the failure mode a per-format round trip cannot catch:
// with no formats registered there is nothing to round-trip, so a table of zero
// codec rows would read as "nothing to test" rather than "the registry did not
// populate".
func Test_codecRegistryPopulated(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// wantStatus is the outcome the check must report on a sound build.
		wantStatus harness.Status
	}
	tests := []tc{
		{name: "the host's registry", wantStatus: harness.Pass},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := codecRegistryPopulated()

		if got.Status != c.wantStatus {
			t.Fatalf("Status = %v, want %v (detail %q)", got.Status, c.wantStatus, got.Detail)
		}
		if got.Domain != codecDomain || got.Name == "" || got.Detail == "" {
			t.Fatalf("the Result is not diagnosable from the table: %+v", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
