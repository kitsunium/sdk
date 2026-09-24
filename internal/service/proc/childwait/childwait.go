// Package childwait decides who receives a child's exit status (ADR 0093).
//
// A process has two kinds of waiter for its children. The Process handle
// returned by service/proc/exec waits for the one child it spawned. The reaper
// (service/proc/reaper), switched on when the supervisor runs as pid 1 or as a
// subreaper, answers every SIGCHLD with wait4(-1) — which collects ANY child,
// the handle's included. The kernel hands a zombie's status to exactly one
// wait, so before this package whichever call reached the kernel first kept it:
// when the sweep won, the handle's own wait failed with ECHILD and an exit 0
// was reported as WAIT_FAILED. A supervisor read that as a failure and
// restarted a service that had stopped cleanly.
//
// The ledger makes every status reach its owner whoever collects it:
//
//   - Spawn forks and CLAIMS the child in one step. The fork runs under a
//     shared gate and the pid is claimed before the gate is released, so no
//     sweep can decide a child is nobody's while its spawn is in flight.
//   - ReapAny is the only wait4(-1) in the SDK. A child it collects that is
//     claimed has its status stored on the claim, under a lock held from before
//     the wait4 until after the hand-off. The claim is looked up only once no
//     spawn is between fork and claim, so a child that exited before its spawn
//     registered it is found, and a claim left on a recycled pid has already
//     been replaced by the newer child's. A child nobody claims is an orphan.
//   - The owner still waits for its own child itself, so a process without a
//     reaper behaves exactly as before. When that wait fails with ECHILD, the
//     owner takes the sweep lock — any hand-off still in progress lands first —
//     and reads the status from its claim. A status stored before the owner
//     ever waited is read first, so it is never waited for at all.
//
// A status is lost only when something outside the SDK reaps the child, and
// the owner then learns it deterministically instead of hanging.
package childwait

import (
	"os"
	"sync"
)

// Claim is a spawner's hold on one child's exit status. It is created by Spawn,
// filled by a sweep that collects the child, and ended by the owner once the
// status is in hand. A Claim is safe for concurrent use. A nil *Claim holds
// nothing: every method answers as for a claim no sweep ever filled.
type Claim struct {
	// book is the ledger that holds this claim.
	book *ledger
	// pid is the claimed child's process id, and the ledger key.
	pid int
	// collected reports that a sweep took the child and stored its status. It
	// and status are guarded by the ledger's mu.
	collected bool
	// status is the exit status the sweep collected, valid once collected.
	status StatusValue
}

// ledger is the process-wide record of claimed children. There is exactly one,
// because wait4(-1) collects the children of the whole process.
type ledger struct {
	// mu guards claims and every Claim's collected and status fields.
	mu sync.Mutex
	// claims maps each claimed, not yet collected child's pid to its claim.
	claims map[int]*Claim
	// spawning is held shared from before a fork until its child is claimed. A
	// sweep holding a pid nobody claims takes it exclusively, which waits out
	// every spawn that may have forked that pid without claiming it yet.
	spawning sync.RWMutex
	// sweeping is held from before one wait4(-1) until what it collected has
	// been handed over, so an owner whose own wait failed with ECHILD can wait
	// for the hand-off by taking it.
	sweeping sync.Mutex
}

// book is the process's single ledger.
var book = newLedger()

// newLedger returns an empty ledger.
func newLedger() *ledger {
	//: nothing is claimed before the first spawn.
	return &ledger{claims: make(map[int]*Claim)}
}

// Spawn runs start — a fork/exec returning the started process — and claims
// the child before any sweep can treat it as an orphan. start's error is
// returned verbatim, with no process and no claim. The caller owns the claim
// and must end it through Reclaim or Release once it has the exit status.
func Spawn(start func() (*os.Process, error)) (proc *os.Process, claim *Claim, err error) {
	//: delegate to the process-wide ledger.
	return book.spawn(start)
}

// spawn forks under the shared gate and claims the child before releasing it.
func (l *ledger) spawn(start func() (*os.Process, error)) (proc *os.Process, claim *Claim, err error) {
	//: a sweep holding an unclaimed pid waits until this spawn has claimed its
	//: child, so the pid cannot be taken for an orphan in between.
	l.spawning.RLock()
	//: release the gate on every path, once the child is claimed or never was.
	defer l.spawning.RUnlock()
	proc, err = start()
	//: a failed fork/exec leaves no child to claim.
	if err != nil {
		//: hand the spawn error back untouched.
		return nil, nil, err
	}
	//: the child exists; claim it while the gate still holds sweeps back.
	return proc, l.track(proc.Pid), nil
}

// track claims pid. A claim already held for the same pid is stale — a new
// child can only have that pid once the old one was reaped — so it is replaced.
func (l *ledger) track(pid int) *Claim {
	claim := &Claim{book: l, pid: pid}
	//: the map and the claims it holds are guarded by mu.
	l.mu.Lock()
	l.claims[pid] = claim
	l.mu.Unlock()
	//: the owner keeps the claim to read or end it.
	return claim
}

// deliver hands the status a sweep collected for pid to the claim on pid, if
// any. The lookup happens only once every in-flight spawn has claimed its
// child: the zombie just collected is the NEWEST process to hold pid, and if a
// spawn forked it, that spawn's claim must be in place — replacing any older
// claim on a recycled pid — before the lookup can say whose status this is.
// A pid still unclaimed then was an orphan's, and its status has no owner.
func (l *ledger) deliver(pid int, status StatusValue) {
	//: wait out every spawn that may have forked pid without claiming it yet.
	l.spawning.Lock()
	//: reopen the gate once the lookup below is conclusive.
	defer l.spawning.Unlock()
	//: with no spawn in flight, the claim on pid is the collected child's.
	l.handOver(pid, status)
}

// handOver stores status on the claim for pid and retires the claim from the
// ledger, reporting whether a claim existed.
func (l *ledger) handOver(pid int, status StatusValue) bool {
	//: the claim and its fields are guarded by mu.
	l.mu.Lock()
	//: release mu on both paths.
	defer l.mu.Unlock()
	claim, ok := l.claims[pid]
	//: nobody claims this pid (yet, or at all).
	if !ok {
		//: report the miss so the caller can decide what it means.
		return false
	}
	claim.collected = true
	claim.status = status
	//: the pid is free now; a later child with the same pid is someone else.
	delete(l.claims, pid)
	//: the owner will find the status on its claim.
	return true
}

// Collected reports the status a sweep handed over, if one has. An owner asks
// before waiting itself: a child already collected must not be waited for by
// pid, because the pid may by now belong to another process.
func (c *Claim) Collected() (status StatusValue, ok bool) {
	//: a nil claim was never filled.
	if c == nil {
		//: nothing collected.
		return StatusValue{}, false
	}
	//: the claim's fields are guarded by the ledger's mu.
	c.book.mu.Lock()
	//: release mu once the snapshot is taken.
	defer c.book.mu.Unlock()
	//: a copy of the stored status, and whether there is one.
	return c.status, c.collected
}

// Reclaim is for an owner whose own wait failed with ECHILD — the child was
// reaped by someone else. It waits for any sweep in progress to finish its
// hand-off, then reports the status the ledger collected. ok false means
// nothing in the SDK took the child: something outside it reaped the child and
// the status is lost. Either way the claim is ended.
func (c *Claim) Reclaim() (status StatusValue, ok bool) {
	//: a nil claim was never in the ledger, so no sweep could fill it.
	if c == nil {
		//: nothing to reclaim: the status is lost.
		return StatusValue{}, false
	}
	//: a sweep that collected our child holds sweeping until the hand-off is
	//: done; our ECHILD came after that collection, so taking the lock orders
	//: us after the hand-off.
	c.book.sweeping.Lock()
	//: let the next sweep run once the claim has been read.
	defer c.book.sweeping.Unlock()
	//: read the claim and retire it in one critical section.
	c.book.mu.Lock()
	//: release mu once the claim is read and retired.
	defer c.book.mu.Unlock()
	c.book.forget(c)
	//: whatever a sweep stored, if it stored anything.
	return c.status, c.collected
}

// Release ends the claim: the owner has the exit status from its own wait, or
// has given up on it. A later sweep that collects the same pid belongs to a
// different child and must not land on this claim.
func (c *Claim) Release() {
	//: a nil claim is in no ledger.
	if c == nil {
		//: nothing to remove.
		return
	}
	//: the map is guarded by mu.
	c.book.mu.Lock()
	c.book.forget(c)
	c.book.mu.Unlock()
}

// forget removes c from the ledger if it is still the claim on its pid. The
// caller holds mu. A claim a sweep already collected, or that a newer child's
// claim replaced, is left alone.
func (l *ledger) forget(c *Claim) {
	//: only remove the entry when it is this very claim.
	if l.claims[c.pid] == c {
		delete(l.claims, c.pid)
	}
}
