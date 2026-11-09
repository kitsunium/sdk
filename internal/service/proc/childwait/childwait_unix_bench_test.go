//go:build unix

package childwait

import (
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// errSink and pidSink keep the benchmarked results alive.
var (
	errSink error
	pidSink int
)

// benchTrue is the tiny child the spawn rows fork.
const benchTrue string = "/bin/true"

// claimsLarge is the ledger size of the 10 000-claim rows.
const claimsLarge int = 10_000

// fakePidBase numbers the claims a row plants: far above any pid_max, so no
// real child ever matches one.
const fakePidBase int = 1 << 30

// ledgerWithClaims returns a private ledger holding n planted claims.
func ledgerWithClaims(n int) *ledger {
	l := newLedger()
	for i := range n {
		l.track(fakePidBase + i)
	}
	return l
}

// BenchmarkReapAny_NoClaims is one sweep turn with nothing to collect: the
// wait4(-1, WNOHANG) a reaper's loop pays per SIGCHLD when the child it was
// woken for is already gone.
func BenchmarkReapAny_NoClaims(b *testing.B) {
	l := newLedger()
	b.ReportAllocs()
	for b.Loop() {
		pidSink, errSink = l.reapAny()
	}
}

// BenchmarkDeliver_NoClaims and BenchmarkDeliver_10kClaims are the hand-off of
// one collected status — the spawn gate taken exclusively, the claim looked up —
// on an empty ledger and on one holding 10 000 claims: a map lookup, so the
// size should not show.
func BenchmarkDeliver_NoClaims(b *testing.B) {
	benchDeliver(b, 0)
}

// BenchmarkDeliver_10kClaims is BenchmarkDeliver_NoClaims with 10 000 claims.
func BenchmarkDeliver_10kClaims(b *testing.B) {
	benchDeliver(b, claimsLarge)
}

// benchDeliver delivers an unclaimed pid's status on a ledger of n claims.
func benchDeliver(b *testing.B, n int) {
	b.Helper()
	l := ledgerWithClaims(n)
	status := &StatusValue{}
	b.ReportAllocs()
	for b.Loop() {
		l.deliver(fakePidBase-1, status)
	}
}

// BenchmarkSpawn_ClaimOverhead is what Spawn adds around a fork: the shared
// gate and the claim, with a start that forks nothing (it hands back this
// process's own handle) and the claim released as an owner's wait would.
func BenchmarkSpawn_ClaimOverhead(b *testing.B) {
	l := newLedger()
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		b.Fatalf("os.FindProcess = %v", err)
	}
	start := func() (*os.Process, error) { return self, nil }
	b.ReportAllocs()
	for b.Loop() {
		_, claim, sErr := l.spawn(start)
		errSink = sErr
		claim.Release()
	}
}

// BenchmarkSpawnSweep_Contention forks /bin/true through the ledger and
// reports the spawn's p50 and p99 (fork + exec, until StartProcess returns)
// with no reaper, then with one draining wait4(-1) in a loop beside it — the
// gate both take. The children are collected by their own wait or by the
// sweep, through the claim.
func BenchmarkSpawnSweep_Contention(b *testing.B) {
	if _, err := os.Stat(benchTrue); err != nil {
		b.Skipf("%s: %v", benchTrue, err)
	}
	for _, reaping := range []bool{false, true} {
		name := "no_reaper"
		if reaping {
			name = "reaper_running"
		}
		b.Run(name, func(b *testing.B) {
			benchContention(b, reaping)
		})
	}
}

// benchContention is one row of BenchmarkSpawnSweep_Contention.
func benchContention(b *testing.B, reaping bool) {
	b.Helper()
	var stop atomic.Bool
	var wg sync.WaitGroup
	//: the reaper: a sweep loop, as reaper.Start runs on SIGCHLD.
	if reaping {
		wg.Go(func() {
			for !stop.Load() {
				if pid, _ := book.reapAny(); pid <= 0 {
					time.Sleep(50 * time.Microsecond)
				}
			}
		})
	}
	attr := &os.ProcAttr{Files: []*os.File{nil, nil, nil}}
	lat := make([]time.Duration, 0, 1024)
	for b.Loop() {
		t0 := time.Now()
		proc, claim, err := book.spawn(func() (*os.Process, error) { return os.StartProcess(benchTrue, []string{"true"}, attr) })
		lat = append(lat, time.Since(t0))
		if err != nil {
			b.Fatalf("spawn = %v", err)
		}
		reapOwn(proc, claim)
	}
	stop.Store(true)
	wg.Wait()
	slices.Sort(lat)
	b.ReportMetric(float64(lat[len(lat)/2].Nanoseconds()), "p50-ns")
	b.ReportMetric(float64(lat[len(lat)*99/100].Nanoseconds()), "p99-ns")
}

// reapOwn collects a benchmark child: its own wait, or the sweep's hand-off
// when the sweep took it first (ECHILD).
func reapOwn(proc *os.Process, claim *Claim) {
	if _, ok := claim.Collected(); ok {
		errSink = proc.Release()
		return
	}
	if _, err := proc.Wait(); err == nil {
		claim.Release()
		return
	}
	if _, ok := claim.Reclaim(); !ok {
		claim.Release()
	}
	errSink = proc.Release()
}
