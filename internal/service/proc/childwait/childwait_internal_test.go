// Package childwait — white-box tests of the ledger's races, each driven on a
// private ledger with a fabricated pid, so no real process and no real sweep is
// involved and every interleaving is forced rather than hoped for.
package childwait

import (
	"errors"
	"os"
	"testing"
	"time"
)

// settle is how long a test waits to establish that a call has NOT returned.
// It can only produce a false pass, never a false failure: a correct ledger
// cannot return inside it, however slow the machine.
const settle time.Duration = 50 * time.Millisecond

// fakePid is the pid every test fabricates. Nothing is ever signalled or
// waited for by it; it is only a ledger key.
const fakePid int = 4242

// errFork is the fork failure fed to spawn by the failure case.
var errFork = errors.New("fork refused")

// fakeProcess returns a process value carrying pid and nothing else. The
// ledger reads only the pid, so no method of it is ever called.
func fakeProcess(pid int) *os.Process {
	//: a bare literal: the ledger never touches the process itself.
	return &os.Process{Pid: pid}
}

// pending reports how many claims l still holds.
func pending(l *ledger) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.claims)
}

// spawnInFlight starts a spawn of pid on l whose fork has happened but whose
// claim has not been registered yet. It returns once the fork is in, with a
// function that lets the spawn finish and returns the claim it registered.
//
// Goroutine lifecycle: one goroutine per call runs the spawn. It is parked in
// the scripted fork until finish releases it, and finish returns only after
// it has sent its claim — so no spawn outlives the helper's caller.
func spawnInFlight(t *testing.T, l *ledger, pid int) (finish func() *Claim) {
	t.Helper()
	forked := make(chan struct{})
	release := make(chan struct{})
	claimed := make(chan *Claim, 1)
	go func() {
		//: the fork has happened and the pid exists, but it is not claimed.
		_, claim, err := l.spawn(func() (*os.Process, error) {
			close(forked)
			<-release
			return fakeProcess(pid), nil
		})
		//: the scripted fork cannot fail, so an error is the ledger's own.
		if err != nil {
			t.Errorf("spawn of pid %d: %v", pid, err)
		}
		claimed <- claim
	}()
	<-forked
	//: releasing the spawn lets it claim the pid and return.
	return func() *Claim {
		close(release)
		return <-claimed
	}
}

// deliverAsync runs a sweep's hand-off of pid on its own goroutine and returns
// a channel closed once it has returned.
func deliverAsync(l *ledger, pid int) <-chan struct{} {
	delivered := make(chan struct{})
	go func() {
		l.deliver(pid, &StatusValue{})
		close(delivered)
	}()
	//: the caller watches the channel to learn when the hand-off returned.
	return delivered
}

// requireBlocked fails the test if done closes within settle.
func requireBlocked(t *testing.T, done <-chan struct{}, why string) {
	t.Helper()
	//: a hand-off that returns here decided before it could know the answer.
	select {
	//: returned too early — the defect under test.
	case <-done:
		t.Fatal(why)
	//: still waiting, as it must.
	case <-time.After(settle):
	}
}

// TestDeliverWaitsForASpawnInFlight pins the first race: a child can exit, and
// a sweep collect it, before the spawn that forked it has claimed it. A sweep
// that concluded "orphan" at that moment would give the status to nobody, and
// the Process would later report a clean exit as WAIT_FAILED.
func TestDeliverWaitsForASpawnInFlight(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a child collected between its fork and its claim"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		l := newLedger()
		finish := spawnInFlight(t, l, fakePid)
		delivered := deliverAsync(l, fakePid)
		requireBlocked(t, delivered, c.name+": deliver returned while the spawn that forked the pid had not claimed it — the status went to nobody")
		claim := finish()
		<-delivered
		//: once the spawn claimed the pid, the waiting sweep handed it over.
		if _, ok := claim.Collected(); !ok {
			t.Errorf("%s: the status a sweep collected never reached the claim", c.name)
		}
		//: a collected claim is retired from the ledger.
		if n := pending(l); n != 0 {
			t.Errorf("%s: %d claims left in the ledger, want 0 — a collected claim is retired", c.name, n)
		}
	}
	//: run every case as its own parallel subtest.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDeliverSkipsAClaimTheRecycledPidOutlived pins the same gate from the
// other side. A claim can outlive its child when something outside the SDK
// reaps the child before its owner waits; once the pid is recycled for a new
// SDK child that exits before its spawn has claimed it, a hand-off that looked
// the pid up at once would give the NEW child's status to the OLD claim —
// one Process reporting another's exit, and the new one's lost.
func TestDeliverSkipsAClaimTheRecycledPidOutlived(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a stale claim on a pid a spawn in flight has just forked"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		l := newLedger()
		//: the old child's claim, never reclaimed: an outsider reaped it.
		stale := l.track(fakePid)
		finish := spawnInFlight(t, l, fakePid)
		delivered := deliverAsync(l, fakePid)
		requireBlocked(t, delivered, c.name+": deliver answered while the spawn of the recycled pid was in flight — it could only have used the stale claim")
		fresh := finish()
		<-delivered
		//: the status is the new child's and must land on the new claim.
		if _, ok := fresh.Collected(); !ok {
			t.Errorf("%s: the new child's claim did not receive its status", c.name)
		}
		//: the stale claim must not have been handed a stranger's exit.
		if _, ok := stale.Collected(); ok {
			t.Errorf("%s: the stale claim received the status of the process that recycled its pid", c.name)
		}
		stale.Release()
		//: nothing is left behind once both owners are done.
		if n := pending(l); n != 0 {
			t.Errorf("%s: %d claims left in the ledger, want 0", c.name, n)
		}
	}
	//: run every case as its own parallel subtest.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestReclaimWaitsForTheHandOver pins the second race: the owner's own wait
// fails with ECHILD the instant the kernel hands the zombie to the sweep, which
// is BEFORE the sweep has returned to hand the status over. An owner reading
// its claim at that instant would find it empty and call the status lost.
//
// Goroutine lifecycle: each case runs the owner's Reclaim on one goroutine,
// which blocks on the sweep lock the case holds and ends once the case hands
// the status over and releases it; the case reads its verdict before
// returning, so the goroutine never outlives it.
func TestReclaimWaitsForTheHandOver(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"an owner whose wait saw ECHILD mid-sweep"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		l := newLedger()
		claim := l.track(fakePid)
		//: the test is the sweep: it has collected the child and not yet handed
		//: the status over.
		l.sweeping.Lock()
		reclaimed := make(chan bool, 1)
		done := make(chan struct{})
		go func() {
			_, ok := claim.Reclaim()
			reclaimed <- ok
			close(done)
		}()
		requireBlocked(t, done, c.name+": Reclaim answered while the sweep that took the child was still handing it over")
		l.handOver(fakePid, &StatusValue{})
		l.sweeping.Unlock()
		//: the owner now finds the status the sweep collected.
		if ok := <-reclaimed; !ok {
			t.Errorf("%s: Reclaim reported the status lost although the sweep handed it over", c.name)
		}
	}
	//: run every case as its own parallel subtest.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestTheLedgerEndsEveryClaim pins what each path leaves behind. A claim that
// outlived its child would receive the status of the next process to reuse
// the pid; an orphan's status kept "in case" would be handed to that process
// too. So every path retires the claim, and nothing unclaimed is ever stored.
func TestTheLedgerEndsEveryClaim(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// run drives the ledger and returns the claim to inspect, or nil.
		run func(l *ledger) *Claim
		// collected is whether the inspected claim must hold a status.
		collected bool
	}
	tests := []tc{
		{"the owner's own wait collected the child", func(l *ledger) *Claim {
			c := l.track(fakePid)
			c.Release()
			return c
		}, false},
		{"a sweep collected the child", func(l *ledger) *Claim {
			c := l.track(fakePid)
			l.deliver(fakePid, &StatusValue{})
			return c
		}, true},
		{"something outside the SDK collected the child", func(l *ledger) *Claim {
			c := l.track(fakePid)
			//: nothing delivered: the owner's ECHILD finds an empty claim.
			if _, ok := c.Reclaim(); ok {
				return nil
			}
			return c
		}, false},
		{"an orphan nobody claimed", func(l *ledger) *Claim {
			l.deliver(fakePid, &StatusValue{})
			//: a child spawned later with the recycled pid starts clean.
			return l.track(fakePid)
		}, false},
		{"a stale claim released after the pid was reused", func(l *ledger) *Claim {
			stale := l.track(fakePid)
			fresh := l.track(fakePid)
			stale.Release()
			//: the release must not have dropped the newer child's claim.
			l.deliver(fakePid, &StatusValue{})
			//: the stale claim must not be the one that was filled.
			if _, ok := stale.Collected(); ok {
				return nil
			}
			return fresh
		}, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		l := newLedger()
		claim := c.run(l)
		//: a nil claim means a status reached a claim it did not belong to.
		if claim == nil {
			t.Fatalf("%s: a status reached a claim it did not belong to", c.name)
		}
		//: the claim holds a status exactly when a sweep delivered one to it.
		if _, ok := claim.Collected(); ok != c.collected {
			t.Errorf("%s: Collected() = %t, want %t", c.name, ok, c.collected)
		}
		//: the orphan case re-claims the pid on purpose; end that claim first.
		claim.Release()
		//: every path leaves the ledger empty.
		if n := pending(l); n != 0 {
			t.Errorf("%s: %d claims left in the ledger, want 0", c.name, n)
		}
	}
	//: run every case as its own parallel subtest.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestSpawnReturnsTheForkError pins that a failed fork leaves no claim: there
// is no child whose status could ever be delivered.
func TestSpawnReturnsTheForkError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a fork/exec that failed"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		l := newLedger()
		proc, claim, err := l.spawn(func() (*os.Process, error) {
			return nil, errFork
		})
		//: the start error comes back untouched.
		if !errors.Is(err, errFork) {
			t.Errorf("%s: spawn error = %v, want %v", c.name, err, errFork)
		}
		//: no process and no claim beside an error.
		if proc != nil || claim != nil {
			t.Errorf("%s: spawn returned a process or a claim beside its error", c.name)
		}
		//: nothing was claimed.
		if n := pending(l); n != 0 {
			t.Errorf("%s: %d claims left in the ledger, want 0", c.name, n)
		}
	}
	//: run every case as its own parallel subtest.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestANilClaimHoldsNothing pins the nil receiver: a handle assembled without
// the ledger — a test double, a zero value — must degrade to "no sweep ever
// filled this claim" rather than panic in the middle of a Wait.
func TestANilClaimHoldsNothing(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a nil claim"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var claim *Claim
		//: a nil claim was never filled.
		if _, ok := claim.Collected(); ok {
			t.Errorf("%s: Collected() reported a status", c.name)
		}
		//: and nothing can be reclaimed from it.
		if _, ok := claim.Reclaim(); ok {
			t.Errorf("%s: Reclaim() reported a status", c.name)
		}
		//: releasing nothing must be a no-op, not a panic.
		claim.Release()
	}
	//: run every case as its own parallel subtest.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
