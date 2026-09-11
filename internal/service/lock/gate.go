// Package lock — the in-process gate the file locker takes BEFORE flock(2),
// because flock alone excludes nothing between goroutines.
package lock

import (
	"context"
	"sync"
)

// nameGate is a per-name, context-aware, non-expiring mutual exclusion between
// goroutines of one process.
//
// # Why it exists, measured rather than assumed
//
// flock(2) is per OPEN FILE DESCRIPTION, not per process and not per thread.
// Two consequences follow, and they pull in opposite directions:
//
//   - Two separate open(2) calls on the same path, in the SAME process, DO
//     exclude each other. Measured on linux/amd64 (kernel 6.12): the second
//     LOCK_EX|LOCK_NB returns EWOULDBLOCK and a blocking LOCK_EX waits.
//   - The same descriptor re-locked is a NO-OP. flock(LOCK_EX) on a
//     description that already holds LOCK_EX is a lock CONVERSION: it returns
//     success immediately. Measured on the same host with eight goroutines
//     sharing one descriptor around a counted critical section: all eight were
//     inside simultaneously. Not "occasionally" — the maximum observed
//     occupancy was 8 of 8, every run.
//
// The second measurement is the one that matters, because holding one
// descriptor for the store's lifetime is the natural, efficient design — it is
// what internal/service/session's file store does — and it makes flock a
// complete no-op between goroutines while continuing to work perfectly between
// processes. The bug is therefore invisible to any test that spawns processes
// and only appears under goroutine concurrency, which is the opposite of where
// anyone looks for a file-locking defect.
//
// The gate removes the question. The file locker takes the gate first and the
// flock second, so goroutine exclusion is guaranteed by Go and process
// exclusion is guaranteed by the kernel, and neither is inferred from the
// other's behaviour on one platform.
//
// # Why it does not expire
//
// The gate's lifetime must match the flock's exactly. A gate that expired
// while its holder still owned the flock would let a second goroutine of the
// same process pass the gate and then block on the flock its own process
// holds — a lock the first goroutine can only release by finishing, which it
// may be waiting on the second to do. So the gate has no TTL, matching the
// file locker's own decision that its leases do not expire.
type nameGate struct {
	// entries maps a held name to a channel closed when it is released. The
	// channel is the wake-up: a waiter selects on it and on its own context.
	entries map[string]chan struct{}
	// mu guards entries. It is held only for map operations, never across a
	// wait.
	mu sync.Mutex
}

// newNameGate builds an empty gate.
func newNameGate() *nameGate {
	//: the map is the whole state; the hint matches memory.go's, since a
	//: process guards a handful of named resources rather than thousands.
	return &nameGate{entries: make(map[string]chan struct{}, initialNames)}
}

// tryEnter takes name if it is free. It never waits.
func (g *nameGate) tryEnter(name string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: already held by another goroutine of this process.
	if _, busy := g.entries[name]; busy {
		//: the caller decides what to do about it.
		return false
	}
	//: take it; the channel is what will wake the waiters on release.
	g.entries[name] = make(chan struct{})
	//: entered.
	return true
}

// enter blocks until name is taken by this goroutine or ctx ends.
func (g *nameGate) enter(ctx context.Context, name string) error {
	//: contend, wait for the holder to leave, contend again.
	for {
		//: a cancellation observed first saves a pointless attempt.
		if ctx.Err() != nil {
			//: the caller's own error.
			return ctx.Err()
		}
		g.mu.Lock()
		freed, busy := g.entries[name]
		//: free — take it under the same lock that observed it free.
		if !busy {
			g.entries[name] = make(chan struct{})
			g.mu.Unlock()
			//: entered.
			return nil
		}
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			//: the caller's patience ran out.
			return ctx.Err()
		case <-freed:
			//: the holder left — re-contend rather than take a hand-off, so
			//: no ordering is promised that the caller cannot observe.
		}
	}
}

// leave releases name and wakes every waiter.
func (g *nameGate) leave(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	freed, busy := g.entries[name]
	//: leaving a name this goroutine does not hold is a no-op rather than a
	//: panic: the file locker's own idempotent Release is the only caller, and
	//: it already refuses to release twice.
	if !busy {
		//: nothing to do.
		return
	}
	//: drop it first so a waiter that wakes finds the name free.
	delete(g.entries, name)
	close(freed)
}
