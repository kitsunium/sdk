// Package health_test — the three probes, the three states, and the
// aggregation rule.
package health_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svchealth "github.com/kitsunium/sdk/internal/service/health"
)

// TestAnEmptyRegistryIsLegitimate pins ADR 0031's reading of "no checks": the
// process answering the probe IS the evidence that it is running.
//
// The alternative — refusing, or reporting unhealthy until somebody registers
// something — would make the first, smallest, most common wiring of this
// package the one that takes a replica out of rotation.
func TestAnEmptyRegistryIsLegitimate(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	for _, probe := range []corehealth.Probe{
		corehealth.ProbeStartup, corehealth.ProbeReadiness, corehealth.ProbeLiveness,
	} {
		report := registry.Probe(context.Background(), probe)
		if report.Status != corehealth.StatusHealthy {
			t.Errorf("%v on an empty registry = %v, want healthy", probe, report.Status)
		}
		if len(report.Results) != 0 {
			t.Errorf("%v on an empty registry carries %d results, want none",
				probe, len(report.Results))
		}
	}
}

// TestStartupDisablesLiveness is the anti-restart-loop guarantee.
//
// While a startup check has yet to pass, the liveness probe must report
// healthy WITHOUT running anything. A liveness check that legitimately fails
// mid-startup — a worker pool not yet built — would otherwise restart the
// process on every attempt, forever, and the process would never get far
// enough to pass the check that was failing.
func TestStartupDisablesLiveness(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	var startupCalls, livenessCalls atomic.Int64
	mustAddStartup(t, registry, corehealth.StartupCheckValue{
		Name: "migrations", Check: failingWith(&startupCalls, errDependency),
	})
	mustAddLiveness(t, registry, corehealth.LivenessCheckValue{
		Name: "workers", Check: func() error {
			livenessCalls.Add(1)
			return errDependency
		},
	})
	report := registry.Probe(context.Background(), corehealth.ProbeLiveness)
	if report.Status != corehealth.StatusHealthy {
		t.Errorf("liveness while starting = %v, want healthy — a process still "+
			"coming up is not irrecoverable", report.Status)
	}
	if got := livenessCalls.Load(); got != 0 {
		t.Errorf("the liveness check ran %d times while starting, want 0", got)
	}
	//: and the report says WHY it is healthy, rather than looking like a
	//: registry with nothing in it.
	if got := resultFor(t, report, "starting"); got.Status != corehealth.StatusHealthy {
		t.Errorf("the short-circuit result is %v, want healthy", got.Status)
	}
}

// TestStartupWithholdsReadiness pins the other half: while starting, the
// replica must not be routed to, and no readiness check is dialled to
// reconfirm a decision already taken.
func TestStartupWithholdsReadiness(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	var startupCalls, readyCalls atomic.Int64
	mustAddStartup(t, registry, corehealth.StartupCheckValue{
		Name: "migrations", Check: failingWith(&startupCalls, errDependency),
	})
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
		Name: "db", Check: passing(&readyCalls),
	})
	report := registry.Probe(context.Background(), corehealth.ProbeReadiness)
	if report.Status.Serving() {
		t.Errorf("readiness while starting = %v, want not serving", report.Status)
	}
	if got := readyCalls.Load(); got != 0 {
		t.Errorf("a readiness check ran %d times while starting, want 0 — the "+
			"answer does not depend on any dependency", got)
	}
	result := resultFor(t, report, "starting")
	if !errs.HasCode(result.Err, svchealth.CodeStartupPending) {
		t.Errorf("the short-circuit carries %v, want STARTUP_PENDING", result.Err)
	}
}

// TestAStartupCheckLatches pins that a startup check runs until it passes ONCE
// and then never again — and that passing it releases both other probes.
func TestAStartupCheckLatches(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	var calls atomic.Int64
	failing := true
	mustAddStartup(t, registry, corehealth.StartupCheckValue{
		Name: "migrations", Check: func(_ context.Context) error {
			calls.Add(1)
			//: fails once, then succeeds — the ordinary shape of a startup
			//: check waiting on something that is still coming up.
			if failing {
				return errDependency
			}
			return nil
		},
	})
	first := registry.Probe(context.Background(), corehealth.ProbeStartup)
	if first.Status.Serving() {
		t.Fatalf("the first startup probe = %v, want not serving", first.Status)
	}
	failing = false
	if got := registry.Probe(context.Background(), corehealth.ProbeStartup); !got.Status.Serving() {
		t.Fatalf("the second startup probe = %v, want serving", got.Status)
	}
	//: a third probe must replay rather than re-run: a one-shot repeated at
	//: steady state is either wrong or expensive, and usually both.
	third := registry.Probe(context.Background(), corehealth.ProbeStartup)
	if got := calls.Load(); got != 2 {
		t.Errorf("the startup check ran %d times, want 2 — it must latch", got)
	}
	if !resultFor(t, third, "migrations").Cached {
		t.Error("the latched result is not marked cached; a replay must say it is one")
	}
	//: and readiness is released now that nothing is pending.
	if got := registry.Probe(context.Background(), corehealth.ProbeReadiness); !got.Status.Serving() {
		t.Errorf("readiness after startup completed = %v, want serving", got.Status)
	}
}

// TestDrainWithdrawsRoutingButKeepsAnswering is the reason this domain hooks
// into lifecycle at all.
//
// After Drain, readiness must report not-ready without running a check, so an
// orchestrator withdraws the replica; and liveness must KEEP answering
// normally, so the same orchestrator does not kill it while it finishes the
// work the withdrawal was meant to let it finish.
func TestDrainWithdrawsRoutingButKeepsAnswering(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	var readyCalls, liveCalls atomic.Int64
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
		Name: "db", Check: passing(&readyCalls),
	})
	mustAddLiveness(t, registry, corehealth.LivenessCheckValue{
		Name: "workers", Check: func() error {
			liveCalls.Add(1)
			return nil
		},
	})
	registry.Drain()
	ready := registry.Probe(context.Background(), corehealth.ProbeReadiness)
	if ready.Status.Serving() {
		t.Errorf("readiness after Drain = %v, want not serving", ready.Status)
	}
	if got := readyCalls.Load(); got != 0 {
		t.Errorf("a readiness check ran %d times while draining, want 0", got)
	}
	if !errs.HasCode(resultFor(t, ready, "draining").Err, svchealth.CodeDraining) {
		t.Error("the readiness short-circuit does not carry DRAINING")
	}
	live := registry.Probe(context.Background(), corehealth.ProbeLiveness)
	if live.Status != corehealth.StatusHealthy || liveCalls.Load() != 1 {
		t.Errorf("liveness while draining = %v after %d calls, want healthy after 1 — "+
			"a draining replica must be withdrawn, not killed", live.Status, liveCalls.Load())
	}
	//: and it is one-way: a second Drain changes nothing, and there is no
	//: method that could change it back.
	registry.Drain()
	if registry.Probe(context.Background(), corehealth.ProbeReadiness).Status.Serving() {
		t.Error("readiness recovered after a second Drain; draining is terminal")
	}
}

// TestCriticalityDecidesHowBadAFailureIs pins the aggregation rule and the two
// things that make NonCritical safe to offer: a degraded set still serves, and
// degraded never masks unhealthy.
func TestCriticalityDecidesHowBadAFailureIs(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		nonCritical bool
		alsoFail    bool
		want        corehealth.Status
		wantServing bool
	}
	tests := []tc{
		{"a critical failure sinks the probe", false, false, corehealth.StatusUnhealthy, false},
		{"a non-critical failure only degrades it", true, false, corehealth.StatusDegraded, true},
		{"degraded does not mask a critical failure", true, true, corehealth.StatusUnhealthy, false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			registry, _ := newRegistry(t, svchealth.Config{})
			var calls atomic.Int64
			mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
				Name: "cache", Check: failingWith(&calls, errDependency), NonCritical: c.nonCritical,
			})
			if c.alsoFail {
				mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
					Name: "db", Check: failingWith(&calls, errDependency),
				})
			}
			report := registry.Probe(context.Background(), corehealth.ProbeReadiness)
			if report.Status != c.want {
				t.Errorf("aggregate = %v, want %v", report.Status, c.want)
			}
			if report.Status.Serving() != c.wantServing {
				t.Errorf("Serving() = %v, want %v", report.Status.Serving(), c.wantServing)
			}
		})
	}
}

// TestResultsKeepRegistrationOrder pins that two identical probes render
// identically. Completion order would make a diff of two responses unreadable
// and a snapshot test permanently flaky.
func TestResultsKeepRegistrationOrder(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	names := []string{"alpha", "beta", "gamma", "delta"}
	var calls atomic.Int64
	for _, name := range names {
		mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
			Name: name, Check: passing(&calls),
		})
	}
	for range 3 {
		report := registry.Probe(context.Background(), corehealth.ProbeReadiness)
		for i, result := range report.Results {
			if result.Name != names[i] {
				t.Fatalf("result %d is %q, want %q — results must follow registration order",
					i, result.Name, names[i])
			}
		}
	}
}

// TestAnUnknownProbeIsRefusedRatherThanGuessed pins that a Probe value the
// domain never mints does not silently become one that authorises routing or a
// restart.
func TestAnUnknownProbeIsRefusedRatherThanGuessed(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	//: the zero Probe is the realistic form of this mistake.
	report := registry.Probe(context.Background(), corehealth.Probe(0))
	if report.Status.Serving() {
		t.Errorf("an unknown probe = %v, want not serving", report.Status)
	}
	if !errs.HasCode(resultFor(t, report, "unknown").Err, corehealth.CodeUnknownProbe) {
		t.Error("an unknown probe does not carry UNKNOWN_PROBE")
	}
}

// TestRegistrationRefusals pins what cannot be registered, and that each
// refusal names the part that was wrong.
func TestRegistrationRefusals(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	type tc struct {
		name     string
		register func() error
		wantCode errs.Code
	}
	tests := []tc{
		{"a nameless check", func() error {
			return registry.AddReadiness(corehealth.ReadinessCheckValue{Check: passing(nil)})
		}, corehealth.CodeInvalidCheck},
		{"a bodiless readiness check", func() error {
			return registry.AddReadiness(corehealth.ReadinessCheckValue{Name: "x"})
		}, corehealth.CodeInvalidCheck},
		{"a bodiless liveness check", func() error {
			return registry.AddLiveness(corehealth.LivenessCheckValue{Name: "y"})
		}, corehealth.CodeInvalidCheck},
		{"a bodiless startup check", func() error {
			return registry.AddStartup(corehealth.StartupCheckValue{Name: "z"})
		}, corehealth.CodeInvalidCheck},
		{"a cache window past the ceiling", func() error {
			return registry.AddReadiness(corehealth.ReadinessCheckValue{
				Name: "slow", Check: func(context.Context) error { return nil },
				MaxAge: svchealth.MaxCacheAge + time.Second,
			})
		}, svchealth.CodeStaleCacheWindow},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			if err := c.register(); !errs.HasCode(err, c.wantCode) {
				t.Errorf("registration = %v, want code %v", err, c.wantCode)
			}
		})
	}
}

// TestNamesAreUniquePerProbeAndNotGlobally pins both halves: a duplicate on one
// probe is refused, and the same name on two DIFFERENT probes is not — because
// "cache" on readiness and "cache" on liveness are two answers about different
// evidence, not a collision.
func TestNamesAreUniquePerProbeAndNotGlobally(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	var calls atomic.Int64
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{Name: "cache", Check: passing(&calls)})
	err := registry.AddReadiness(corehealth.ReadinessCheckValue{Name: "cache", Check: passing(&calls)})
	if !errs.HasCode(err, corehealth.CodeDuplicateCheck) {
		t.Errorf("a duplicate readiness name = %v, want DUPLICATE_CHECK", err)
	}
	//: the same word, a different question.
	mustAddLiveness(t, registry, corehealth.LivenessCheckValue{
		Name: "cache", Check: func() error { return nil },
	})
}
