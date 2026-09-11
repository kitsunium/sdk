// Package health_test — the port as a consumer sees it, and the executable
// form of this domain's central rule.
package health_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
)

// TestLivenessCannotExpressADependencyCheck is this domain's central claim,
// executable.
//
// The claim is that a dependency check cannot be attached to the liveness
// probe by distraction — the mistake that turns a slow dependency into a
// restart loop and a restart loop into a cascading outage. The mechanism is
// the SHAPE of the three registration types, so the shape is what is asserted:
// a liveness body takes no context, and a context is what every API that
// leaves the process requires.
//
// This test is not decoration for the doc comment. If somebody widens
// LivenessCheckValue.Check to func(context.Context) error, every prose
// paragraph in this package stays true-looking and this test goes red.
func TestLivenessCannotExpressADependencyCheck(t *testing.T) {
	t.Parallel()
	ctxType := reflect.TypeFor[context.Context]()
	liveness, ok := reflect.TypeFor[corehealth.LivenessCheckValue]().FieldByName("Check")
	if !ok {
		t.Fatal("LivenessCheckValue has no Check field")
	}
	//: no parameters at all is the whole mechanism: there is nothing to hand
	//: a deadline to, so a ctx-taking dependency API cannot be assigned here.
	if got := liveness.Type.NumIn(); got != 0 {
		t.Errorf("LivenessCheckValue.Check takes %d parameters, want 0 — a liveness "+
			"body that can accept a context can accept a dependency call", got)
	}
	for _, name := range []string{"ReadinessCheckValue", "StartupCheckValue"} {
		field := checkField(t, name)
		//: the counterpart half: the two probes that MAY call out take a
		//: context, so a dependency check has exactly one place to go.
		if field.Type.NumIn() != 1 || field.Type.In(0) != ctxType {
			t.Errorf("%s.Check is %v, want func(context.Context) error", name, field.Type)
		}
	}
}

// TestTheAbsencesAreDeliberate pins the fields the three registrations
// deliberately do NOT carry.
//
// Each absence is a decision that would otherwise be recoverable only from
// prose: a non-critical liveness check restarts nothing either way, a cached
// liveness answer lets a dead process replay the "alive" it recorded before it
// died, and a startup check that does not gate startup is not a startup check.
// A field added back would make each of those expressible again.
func TestTheAbsencesAreDeliberate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		typ     reflect.Type
		absent  []string
		present []string
	}
	tests := []tc{
		{
			name:   "liveness carries neither criticality nor a cache",
			typ:    reflect.TypeFor[corehealth.LivenessCheckValue](),
			absent: []string{"NonCritical", "MaxAge"}, present: []string{"Name", "Check", "Timeout"},
		},
		{
			name:   "startup carries neither either",
			typ:    reflect.TypeFor[corehealth.StartupCheckValue](),
			absent: []string{"NonCritical", "MaxAge"}, present: []string{"Name", "Check", "Timeout"},
		},
		{
			name:    "readiness carries both",
			typ:     reflect.TypeFor[corehealth.ReadinessCheckValue](),
			present: []string{"Name", "Check", "Timeout", "NonCritical", "MaxAge"},
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			for _, field := range c.absent {
				if _, found := c.typ.FieldByName(field); found {
					t.Errorf("%s grew a %s field; see its doc comment for why it has none",
						c.typ.Name(), field)
				}
			}
			for _, field := range c.present {
				if _, found := c.typ.FieldByName(field); !found {
					t.Errorf("%s lost its %s field", c.typ.Name(), field)
				}
			}
		})
	}
}

// checkField resolves the Check field of one registration type by name.
func checkField(tb testing.TB, name string) reflect.StructField {
	tb.Helper()
	types := map[string]reflect.Type{
		"ReadinessCheckValue": reflect.TypeFor[corehealth.ReadinessCheckValue](),
		"StartupCheckValue":   reflect.TypeFor[corehealth.StartupCheckValue](),
	}
	field, ok := types[name].FieldByName("Check")
	if !ok {
		tb.Fatalf("%s has no Check field", name)
	}
	return field
}

// TestTheZeroStatusIsTheConservativeOne pins ADR 0031 as it applies here: a
// Status that nobody assigned must not read as "everything is fine".
func TestTheZeroStatusIsTheConservativeOne(t *testing.T) {
	t.Parallel()
	var unset corehealth.Status
	if unset != corehealth.StatusUnhealthy {
		t.Errorf("the zero Status is %v, want StatusUnhealthy — a forgotten "+
			"assignment must never authorise routing", unset)
	}
	if unset.Serving() {
		t.Error("the zero Status serves; a forgotten assignment must not")
	}
}

// TestWorstNeverLetsDegradedMaskUnhealthy pins the aggregation rule, including
// the asymmetry that is the whole reason NonCritical is safe to offer.
func TestWorstNeverLetsDegradedMaskUnhealthy(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		a, b corehealth.Status
		want corehealth.Status
	}
	healthy, degraded := corehealth.StatusHealthy, corehealth.StatusDegraded
	unhealthy := corehealth.StatusUnhealthy
	tests := []tc{
		{"two healthy stay healthy", healthy, healthy, healthy},
		{"one degraded degrades the set", healthy, degraded, degraded},
		{"one unhealthy sinks the set", healthy, unhealthy, unhealthy},
		{"degraded does not mask unhealthy", degraded, unhealthy, unhealthy},
		{"the fold is symmetric", unhealthy, degraded, unhealthy},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := corehealth.Worst(c.a, c.b); got != c.want {
				t.Errorf("Worst(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

// TestServingIsTrueForDegraded pins that a non-critical failure keeps the
// replica in rotation. If this flips, "non-critical" becomes decorative: a
// cache outage would remove the fleet from routing exactly as thoroughly as a
// datastore outage.
func TestServingIsTrueForDegraded(t *testing.T) {
	t.Parallel()
	if !corehealth.StatusDegraded.Serving() {
		t.Error("StatusDegraded does not serve; NonCritical then means nothing")
	}
	if !corehealth.StatusHealthy.Serving() {
		t.Error("StatusHealthy does not serve")
	}
	if corehealth.StatusUnhealthy.Serving() {
		t.Error("StatusUnhealthy serves")
	}
}

// TestAStatusNobodyMintedDoesNotServe pins the fail-closed half of Serving. A
// Status is a uint8, so every value past the three is representable — through
// a conversion, a decode, or a Health written outside the SDK — and "anything
// but unhealthy serves" routed traffic to all of them while String rendered
// them "unknown". Unknown is not a serving state.
//
// MUTATION (2026-09-11): Serving put back to `return s != StatusUnhealthy`.
// Observed: `Status(3) serves; a verdict nobody minted must not authorise
// routing`, and the same for 42 and 255. Restored; SHA-256 of
// health_status.go identical to the fixed file.
func TestAStatusNobodyMintedDoesNotServe(t *testing.T) {
	t.Parallel()
	for _, status := range []corehealth.Status{3, 42, 255} {
		if status.Serving() {
			t.Errorf("Status(%d) serves; a verdict nobody minted must not authorise routing", status)
		}
	}
}

// TestStringsAreTheContract pins the spellings. An operator greps them and a
// dashboard parses them, so they are not a formatting detail — and a value the
// package never mints must say "unknown" rather than print a number.
func TestStringsAreTheContract(t *testing.T) {
	t.Parallel()
	statuses := map[corehealth.Status]string{
		corehealth.StatusHealthy: "healthy", corehealth.StatusDegraded: "degraded",
		corehealth.StatusUnhealthy: "unhealthy", corehealth.Status(42): "unknown",
	}
	for status, want := range statuses {
		if got := status.String(); got != want {
			t.Errorf("Status(%d).String() = %q, want %q", status, got, want)
		}
	}
	probes := map[corehealth.Probe]string{
		corehealth.ProbeStartup: "startup", corehealth.ProbeReadiness: "readiness",
		corehealth.ProbeLiveness: "liveness", corehealth.Probe(0): "unknown",
	}
	for probe, want := range probes {
		if got := probe.String(); got != want {
			t.Errorf("Probe(%d).String() = %q, want %q", probe, got, want)
		}
	}
}

// TestAgeNeverReportsAnAnswerFromTheFuture pins the two guards on staleness:
// an unstamped result has no age, and a clock that moved backwards does not
// produce a negative one that would read as "measured later than now".
func TestAgeNeverReportsAnAnswerFromTheFuture(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	type tc struct {
		name string
		at   time.Time
		want time.Duration
	}
	tests := []tc{
		{"an unstamped result has no age", time.Time{}, 0},
		{"a past measurement ages", now.Add(-3 * time.Second), 3 * time.Second},
		{"a measurement in the future floors at zero", now.Add(time.Minute), 0},
		{"a measurement now is not stale", now, 0},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			result := corehealth.ResultValue{At: c.at}
			if got := result.Age(now); got != c.want {
				t.Errorf("Age = %v, want %v", got, c.want)
			}
		})
	}
}
