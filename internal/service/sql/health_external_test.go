// Package sql_test — the health-check suite. Every budget here is asserted by
// advancing a ManualClock, never by sleeping.
package sql_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsql "github.com/kitsunium/sdk/internal/service/sql"
)

// probeBudget is the budget every test in this file arms and then advances
// past. Its value is irrelevant — no wall-clock time elapses — but naming it
// keeps the Advance and the Config from drifting apart.
const probeBudget time.Duration = 3 * time.Second

// newChecker wires a probe over a scripted server and a manual clock.
func newChecker(t *testing.T, f *fakeDB, clk clock.Timed) coresql.Checker {
	t.Helper()
	db := closeOnCleanup(t, f.open())
	checker, err := svcsql.NewChecker(svcsql.Config{
		DB: db, Dialect: coresql.DialectPostgres, Clock: clk,
		Pool: svcsql.PoolConfig{MaxOpen: 2}, CheckTimeout: probeBudget,
	})
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	return checker
}

// TestCheckPassesWhenTheDatabaseAnswers is the ordinary path.
func TestCheckPassesWhenTheDatabaseAnswers(t *testing.T) {
	t.Parallel()
	checker := newChecker(t, newFakeDB(), clock.NewManualClock(time.Unix(0, 0)))
	if err := checker.Check(t.Context()); err != nil {
		t.Fatalf("Check = %v, want nil", err)
	}
}

// TestCheckReportsADriverFailureWithoutRepeatingIt pins the split: the
// verdict is typed and its Public is a fixed literal, while the driver's own
// error stays reachable through the join.
func TestCheckReportsADriverFailureWithoutRepeatingIt(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	f.pingErr = errors.New("dial tcp 10.0.0.7:5432: connection refused")
	checker := newChecker(t, f, clock.NewManualClock(time.Unix(0, 0)))
	err := checker.Check(t.Context())
	if !errs.HasCode(err, svcsql.CodeHealthCheckFailed) {
		t.Fatalf("Check = %v, want HEALTH_CHECK_FAILED", err)
	}
	if !errors.Is(err, f.pingErr) {
		t.Fatal("the driver's own error did not survive the join")
	}
	if public := errs.PublicOf(err); containsString(public, "10.0.0.7") {
		t.Fatalf("Public %q leaked the host from the driver's error", public)
	}
}

// TestCheckTimesOutOnTheInjectedClockWithoutSleeping is the reason this
// package takes a clock.Timed at all. The budget is asserted exactly, with no
// wall-clock time and no tolerance to turn into a flake.
func TestCheckTimesOutOnTheInjectedClockWithoutSleeping(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	//: the probe blocks until the test releases it, so the ONLY thing that
	//: can end the Check is the budget.
	f.pingBlock = make(chan struct{})
	manual := clock.NewManualClock(time.Unix(0, 0))
	checker := newChecker(t, f, manual)
	verdict := make(chan error, 1)
	//: the probe runs on its own goroutine because the whole point is that it
	//: is BLOCKED while the clock advances. The WaitGroup makes the join
	//: explicit rather than implied by the channel receive, so the test
	//: cannot finish with the goroutine still inside Check.
	var probing sync.WaitGroup
	probing.Go(func() { verdict <- checker.Check(t.Context()) })
	t.Cleanup(probing.Wait)
	//: wait for the budget timer to be REGISTERED before advancing, which is
	//: what makes the advance exact rather than racy.
	manual.BlockUntil(1)
	manual.Advance(probeBudget)
	err := <-verdict
	close(f.pingBlock)
	if !errs.HasCode(err, svcsql.CodeHealthCheckTimeout) {
		t.Fatalf("Check = %v, want HEALTH_CHECK_TIMEOUT", err)
	}
}

// TestATimeoutIsADifferentCodeFromARefusal pins the distinction a readiness
// endpoint routes on: a database that says no and one that says nothing are
// different operational facts.
func TestATimeoutIsADifferentCodeFromARefusal(t *testing.T) {
	t.Parallel()
	if svcsql.CodeHealthCheckTimeout == svcsql.CodeHealthCheckFailed {
		t.Fatal("a hang and a refusal share one code")
	}
}

// TestANonPositiveCheckTimeoutClamps pins the clamp side of ADR 0031: an
// unset budget is not "probe with no time at all", which would report a
// timeout for a database that was about to answer.
func TestANonPositiveCheckTimeoutClamps(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	db := closeOnCleanup(t, f.open())
	manual := clock.NewManualClock(time.Unix(0, 0))
	checker, err := svcsql.NewChecker(svcsql.Config{
		DB: db, Dialect: coresql.DialectPostgres, Clock: manual,
		Pool: svcsql.PoolConfig{MaxOpen: 2}, CheckTimeout: 0,
	})
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	//: the clock never advances, so a zero budget read literally would fire
	//: at once and fail this probe.
	if err := checker.Check(t.Context()); err != nil {
		t.Fatalf("Check = %v, want nil — a zero CheckTimeout must clamp, not fire", err)
	}
}

// containsString is a substring test without importing strings for one call.
func containsString(haystack, needle string) bool {
	for start := 0; start+len(needle) <= len(haystack); start++ {
		if haystack[start:start+len(needle)] == needle {
			return true
		}
	}
	return false
}
