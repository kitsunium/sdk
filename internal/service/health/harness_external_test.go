// Package health_test — the shared harness. Every timeout in this suite is
// asserted by MOVING an injected clock, never by sleeping: a sleep would
// become a tolerance, a tolerance a flake, and the flake would eventually take
// the guard on "a slow dependency must not restart the process" with it.
package health_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svchealth "github.com/kitsunium/sdk/internal/service/health"
)

// budget is the per-check budget every registry in this suite is built with.
const budget time.Duration = 2 * time.Second

// base is the fixed instant every manual clock in this suite starts from. A
// literal rather than time.Now() so a failure reproduces byte for byte.
var base = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

// newRegistry builds a registry on a manual clock and hands both back.
func newRegistry(tb testing.TB, cfg svchealth.Config) (corehealth.Health, *clock.ManualClock) {
	tb.Helper()
	//: every check in the suite shares one explicit budget, so an assertion
	//: on a timeout names a number the test itself chose.
	if cfg.DefaultTimeout == 0 {
		cfg.DefaultTimeout = budget
	}
	return newExactRegistry(tb, cfg)
}

// newExactRegistry builds a registry from cfg VERBATIM, injecting only the
// clock. It exists for the one test that asserts what the SDK's own clamps
// resolve to, where the harness supplying a default would assert the harness.
func newExactRegistry(tb testing.TB, cfg svchealth.Config) (corehealth.Health, *clock.ManualClock) {
	tb.Helper()
	clk := clock.NewManualClock(base)
	cfg.Clock = clk
	return svchealth.New(cfg), clk
}

// passing is a check that succeeds and counts its invocations.
func passing(calls *atomic.Int64) corehealth.Check {
	return func(_ context.Context) error {
		calls.Add(1)
		//: nothing to report.
		return nil
	}
}

// failingWith is a check that returns err and counts its invocations.
func failingWith(calls *atomic.Int64, err error) corehealth.Check {
	return func(_ context.Context) error {
		calls.Add(1)
		//: the caller's own error, verbatim.
		return err
	}
}

// blocking is a check that never returns until release is closed, and reports
// whether its context was cancelled first.
//
// It is how a wedged dependency is simulated without any wall-clock wait: the
// registry's budget expires on the manual clock while this body is still
// parked.
func blocking(release <-chan struct{}, cancelled chan<- struct{}) corehealth.Check {
	return func(ctx context.Context) error {
		select {
		//: the budget expired and the registry announced it.
		case <-ctx.Done():
			close(cancelled)
			//: the announcement is what a cooperative check acts on.
			return ctx.Err()
		//: the test let it finish.
		case <-release:
			//: a late but successful answer.
			return nil
		}
	}
}

// wedged is a check that ignores its context entirely and returns only when
// release is closed — a dependency call with no deadline, which is the shape
// the single-flight guard exists for.
//
// It is deliberately NOT the same helper as [blocking]: a check that honours
// its cancellation RETURNS when the budget expires, so the next probe starts a
// fresh run, which is correct. Only a check that ignores it stays outstanding.
func wedged(release <-chan struct{}, calls *atomic.Int64) corehealth.Check {
	return func(_ context.Context) error {
		calls.Add(1)
		<-release
		//: a late answer nobody is waiting for any more.
		return nil
	}
}

// probeUnderClock runs one probe on its own goroutine, waits until the
// registry has armed `waits` budget timers, advances the clock past them, and
// returns the report.
//
// The BlockUntil is the whole reason this is deterministic: it proves the
// timer exists BEFORE the clock is moved, so the advance can never race ahead
// of the arming and produce a probe that simply never times out.
func probeUnderClock(tb testing.TB, registry corehealth.Health, clk *clock.ManualClock,
	probe corehealth.Probe, waits int,
) corehealth.ReportValue {
	tb.Helper()
	reports := make(chan corehealth.ReportValue, 1)
	go func() {
		reports <- registry.Probe(context.Background(), probe)
	}()
	clk.BlockUntil(waits)
	clk.Advance(budget)
	//: the probe returns as soon as every budget it was waiting on has fired.
	return <-reports
}

// resultFor finds one check's result in a report by name.
func resultFor(tb testing.TB, report corehealth.ReportValue, name string) corehealth.ResultValue {
	tb.Helper()
	for _, result := range report.Results {
		if result.Name == name {
			return result
		}
	}
	tb.Fatalf("report for %v carries no result named %q (has %d results)",
		report.Probe, name, len(report.Results))
	return corehealth.ResultValue{}
}

// mustAddReadiness registers a readiness check or fails the test.
func mustAddReadiness(tb testing.TB, registry corehealth.Health, check corehealth.ReadinessCheckValue) {
	tb.Helper()
	if err := registry.AddReadiness(check); err != nil {
		tb.Fatalf("AddReadiness(%q) = %v, want nil", check.Name, err)
	}
}

// mustAddLiveness registers a liveness check or fails the test.
func mustAddLiveness(tb testing.TB, registry corehealth.Health, check corehealth.LivenessCheckValue) {
	tb.Helper()
	if err := registry.AddLiveness(check); err != nil {
		tb.Fatalf("AddLiveness(%q) = %v, want nil", check.Name, err)
	}
}

// mustAddStartup registers a startup check or fails the test.
func mustAddStartup(tb testing.TB, registry corehealth.Health, check corehealth.StartupCheckValue) {
	tb.Helper()
	if err := registry.AddStartup(check); err != nil {
		tb.Fatalf("AddStartup(%q) = %v, want nil", check.Name, err)
	}
}

// errDependency is the plain, infrastructure-shaped error the leak tests use.
// Every substring of it that names infrastructure is asserted absent from a
// probe body.
var errDependency = errors.New("dial tcp 10.0.3.14:5432: connect: connection refused")
