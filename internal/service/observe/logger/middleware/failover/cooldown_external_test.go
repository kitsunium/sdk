package failover_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	corefailover "github.com/kitsunium/sdk/internal/core/observe/logger/middleware/failover"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/failover"
)

// cooldownTestPeriod is the cooldown every test configures.
const cooldownTestPeriod time.Duration = 30 * time.Second

// errBranchDown is what a switchable branch returns while it is down.
var errBranchDown = errors.New("branch down")

// switchBranch is a sink whose health the test flips, counting every Write it
// receives.
type switchBranch struct {
	down  atomic.Bool
	calls atomic.Int64
}

// Write fails while the branch is down and accepts otherwise.
func (b *switchBranch) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	b.calls.Add(1)
	if b.down.Load() {
		return 0, errBranchDown
	}
	return len(p), nil
}

// Flush has nothing to settle.
func (*switchBranch) Flush(context.Context) error { return nil }

// Close has nothing to release.
func (*switchBranch) Close() error { return nil }

// cooled builds a primary/fallback chain with the test cooldown on a manual
// clock.
func cooled(t *testing.T) (corelogger.Sink, *switchBranch, *switchBranch, *clock.ManualClock) {
	t.Helper()
	primary, fallback := &switchBranch{}, &switchBranch{}
	clk := clock.NewManualClock(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
	sink, err := failover.NewWithConfig(failover.Config{Cooldown: cooldownTestPeriod, Clock: clk}, primary, fallback)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	return sink, primary, fallback, clk
}

// write sends one record and fails the test on a refusal.
func write(t *testing.T, sink corelogger.Sink) {
	t.Helper()
	if _, err := sink.Write(t.Context(), corelogger.RecordEvent{}, []byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

// TestCooldownZeroIsNew pins that a zero Config keeps New's behaviour: a
// failing primary is tried on every record.
func TestCooldownZeroIsNew(t *testing.T) {
	t.Parallel()
	primary, fallback := &switchBranch{}, &switchBranch{}
	primary.down.Store(true)
	sink, err := failover.NewWithConfig(failover.Config{}, primary, fallback)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	for range 3 {
		write(t, sink)
	}
	if got := primary.calls.Load(); got != 3 {
		t.Fatalf("primary tried %d times, want 3 — no cooldown without one configured", got)
	}
}

// TestCooldownConfigRefusesAnEmptyChain pins New's refusal on the sibling.
func TestCooldownConfigRefusesAnEmptyChain(t *testing.T) {
	t.Parallel()
	if _, err := failover.NewWithConfig(failover.Config{Cooldown: time.Second}, nil); !errors.Is(err, corefailover.Empty) {
		t.Fatalf("NewWithConfig = %v, want Empty", err)
	}
}

// TestCooldownDemotesAFailedPrimary pins the point: after a failure the
// primary is not retried on every record until the cooldown has passed on the
// clock, and is retried — in its place — once it has.
func TestCooldownDemotesAFailedPrimary(t *testing.T) {
	t.Parallel()
	sink, primary, fallback, clk := cooled(t)
	primary.down.Store(true)
	write(t, sink)
	write(t, sink)
	write(t, sink)
	if got := primary.calls.Load(); got != 1 {
		t.Fatalf("primary tried %d times within the cooldown, want 1", got)
	}
	if got := fallback.calls.Load(); got != 3 {
		t.Fatalf("fallback took %d records, want 3", got)
	}
	primary.down.Store(false)
	clk.Advance(cooldownTestPeriod)
	write(t, sink)
	if got := primary.calls.Load(); got != 2 {
		t.Fatalf("primary tried %d times after the cooldown, want 2", got)
	}
	if got := fallback.calls.Load(); got != 3 {
		t.Fatalf("fallback took %d records, want still 3 — the primary is back in its place", got)
	}
}

// TestCooldownDemotesButNeverSkips pins that a cooldown alone never loses a
// record: when the fallback fails too, the demoted primary is still tried —
// and a primary that recovered takes the record and is back in its place.
func TestCooldownDemotesButNeverSkips(t *testing.T) {
	t.Parallel()
	sink, primary, fallback, _ := cooled(t)
	primary.down.Store(true)
	write(t, sink)
	primary.down.Store(false)
	fallback.down.Store(true)
	write(t, sink)
	if got := primary.calls.Load(); got != 2 {
		t.Fatalf("primary tried %d times, want 2 — demoted, never skipped", got)
	}
	fallback.down.Store(false)
	write(t, sink)
	if got := primary.calls.Load(); got != 3 {
		t.Fatalf("primary tried %d times, want 3 — a recovered branch is back first", got)
	}
}

// TestCooldownTriesEachBranchOnce pins that the second pass reaches only the
// branches that were cooling when the Write began: a branch that has just
// failed in the first pass is not tried a second time for the same record.
func TestCooldownTriesEachBranchOnce(t *testing.T) {
	t.Parallel()
	first, second, third := &switchBranch{}, &switchBranch{}, &switchBranch{}
	clk := clock.NewManualClock(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
	sink, err := failover.NewWithConfig(failover.Config{Cooldown: cooldownTestPeriod, Clock: clk}, first, second, third)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	first.down.Store(true)
	write(t, sink)
	second.down.Store(true)
	third.down.Store(true)
	if _, werr := sink.Write(t.Context(), corelogger.RecordEvent{}, []byte("x")); !errors.Is(werr, corefailover.Exhausted) {
		t.Fatalf("Write = %v, want Exhausted", werr)
	}
	if got := [3]int64{first.calls.Load(), second.calls.Load(), third.calls.Load()}; got != [3]int64{2, 2, 1} {
		t.Fatalf("calls = %v, want [2 2 1] — every branch once per record", got)
	}
}

// TestCooldownExhaustedNamesEveryAttempt pins the verdict when every branch,
// demoted or not, failed: Exhausted, every cause reachable.
func TestCooldownExhaustedNamesEveryAttempt(t *testing.T) {
	t.Parallel()
	sink, primary, fallback, _ := cooled(t)
	primary.down.Store(true)
	fallback.down.Store(true)
	for range 2 {
		_, err := sink.Write(t.Context(), corelogger.RecordEvent{}, []byte("x"))
		if !errors.Is(err, corefailover.Exhausted) || !errors.Is(err, errBranchDown) {
			t.Fatalf("Write = %v, want Exhausted carrying the branches' failures", err)
		}
	}
	if primary.calls.Load() != 2 || fallback.calls.Load() != 2 {
		t.Fatalf("calls = %d/%d, want every branch tried on every record when all are down", primary.calls.Load(), fallback.calls.Load())
	}
}

// TestCooldownUnderConcurrentWrites drives the cooled walk from many
// goroutines while the primary flaps, for the race detector: every record is
// accepted by one branch or the other.
func TestCooldownUnderConcurrentWrites(t *testing.T) {
	t.Parallel()
	sink, primary, fallback, clk := cooled(t)
	const writers, each = 8, 200
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range each {
				if (w+i)%17 == 0 {
					primary.down.Store(!primary.down.Load())
					clk.Advance(time.Second)
				}
				if _, err := sink.Write(context.Background(), corelogger.RecordEvent{}, []byte("x")); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
	if accepted := primary.calls.Load() + fallback.calls.Load(); accepted < writers*each {
		t.Fatalf("branches saw %d writes, want at least %d", accepted, writers*each)
	}
}
