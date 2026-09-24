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
		forked := make(chan struct{})
		release := make(chan struct{})
		claimed := make(chan *Claim, 1)
		go func() {
			//: the fork has happened and the pid exists, but it is not claimed.
			_, claim, _ := l.spawn(func() (*os.Process, error) {
				close(forked)
				<-release
				return fakeProcess(fakePid), nil
			})
			claimed <- claim
		}()
		<-forked
		delivered := make(chan struct{})
		go func() {
			l.deliver(fakePid, Status{})
			close(delivered)
		}()
		//: while the spawn is in flight the sweep must not conclude anything.
		select {
		case <-delivered:
			t.Fatalf("%s: deliver returned while the spawn that forked pid %d had not claimed it — the status went to nobody", c.name, fakePid)
		case <-time.After(settle):
		}
		close(release)
		<-delivered
		claim := <-claimed
		//: once the spawn claimed the pid, the waiting sweep handed it over.
		if _, ok := claim.Collected(); !ok {
			t.Errorf("%s: the status a sweep collected never reached the claim", c.name)
		}
		if n := pending(l); n != 0 {
			t.Errorf("%s: %d claims left in the ledger, want 0 — a collected claim is retired", c.name, n)
		}
	}
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
		go func() {
			_, ok := claim.Reclaim()
			reclaimed <- ok
		}()
		select {
		case <-reclaimed:
			t.Fatalf("%s: Reclaim answered while the sweep that took the child was still handing it over", c.name)
		case <-time.After(settle):
		}
		l.handOver(fakePid, Status{})
		l.sweeping.Unlock()
		//: the owner now finds the status the sweep collected.
		if ok := <-reclaimed; !ok {
			t.Errorf("%s: Reclaim reported the status lost although the sweep handed it over", c.name)
		}
	}
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
			l.deliver(fakePid, Status{})
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
			l.deliver(fakePid, Status{})
			//: a child spawned later with the recycled pid starts clean.
			return l.track(fakePid)
		}, false},
		{"a stale claim released after the pid was reused", func(l *ledger) *Claim {
			stale := l.track(fakePid)
			fresh := l.track(fakePid)
			stale.Release()
			//: the release must not have dropped the newer child's claim.
			l.deliver(fakePid, Status{})
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
		if claim == nil {
			t.Fatalf("%s: a status reached a claim it did not belong to", c.name)
		}
		if _, ok := claim.Collected(); ok != c.collected {
			t.Errorf("%s: Collected() = %t, want %t", c.name, ok, c.collected)
		}
		//: the orphan case re-claims the pid on purpose; end that claim first.
		claim.Release()
		if n := pending(l); n != 0 {
			t.Errorf("%s: %d claims left in the ledger, want 0", c.name, n)
		}
	}
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
		if proc != nil || claim != nil {
			t.Errorf("%s: spawn returned a process or a claim beside its error", c.name)
		}
		if n := pending(l); n != 0 {
			t.Errorf("%s: %d claims left in the ledger, want 0", c.name, n)
		}
	}
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
		if _, ok := claim.Collected(); ok {
			t.Errorf("%s: Collected() reported a status", c.name)
		}
		if _, ok := claim.Reclaim(); ok {
			t.Errorf("%s: Reclaim() reported a status", c.name)
		}
		//: releasing nothing must be a no-op, not a panic.
		claim.Release()
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
