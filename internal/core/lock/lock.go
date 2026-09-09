// Package lock declares the SDK's mutual-exclusion DOMAIN: the [Locker] port
// that hands out named exclusive leases, the [Lease] a holder gets back, and
// the sibling interface through which a lease says whether it can expire at
// all. A core sibling admitted by ADR 0052.
//
// # A lock that lies is worse than no lock
//
// Every decision in this package follows from one observation: a caller who
// believes they hold a critical section behaves differently from a caller who
// knows they do not. A lock that reports success it cannot back removes the
// second behaviour and leaves nothing in its place, so the failure is silent,
// unreproducible, and discovered as data corruption rather than as an error.
// ADR 0031's refuse-rather-than-default rule is therefore not a style choice
// here: a lease TTL of zero is refused AT CONSTRUCTION rather than read as
// "expires immediately", because a lock that expires the instant it is granted
// is exactly the shape of a lock that lies.
//
// # Expiring a lease does not stop its old holder
//
// This is the trap the domain is built around, and it is worth stating in
// full. Suppose A holds a lease with a five-second TTL, and A's goroutine
// stalls — a long GC pause, a blocked syscall, a descheduled thread. Five
// seconds pass. B acquires the same lock, correctly, because the lease has
// expired. A resumes. A has no idea anything happened: nothing interrupted it,
// nothing told it, and its next write lands inside a section B believes it
// owns alone.
//
// No lock service anywhere fixes this by itself, because the lock is not
// holding A — A's own belief is. Three things follow, and this domain decides
// all three rather than leaving them emergent:
//
//  1. OWNERSHIP IS TOKEN-CHECKED. A [Lease] identifies its holder, and
//     [Lease.Release] releases only a lock this holder still holds. A lease
//     that was taken over releases NOTHING and reports [LockNotHeld] — because
//     the alternative, "release whatever is under that name", is A politely
//     unlocking B's critical section.
//  2. A LEASE CAN BE RENEWED. [Lease.Extend] pushes the deadline out for work
//     that outlives its TTL. Without it, a TTL is a bet that the work finishes
//     in time, and the losing side of that bet is silent. A FAILED Extend is
//     not a warning: it is the holder being told it has already lost the lock,
//     and the only correct response is to stop.
//  3. A FENCING TOKEN IS ISSUED. [Lease.Fence] returns a uint64 that strictly
//     increases with every acquisition of that name. Handed to the protected
//     resource and compared there, it turns the scenario above from silent
//     corruption into a rejected write: A carries fence 7, B carries 8, the
//     resource has seen 8, so A's write is refused.
//
// # What this domain does NOT guarantee
//
// Issuing a fencing token is not the same as enforcing one. The SDK can only
// hand the number to the caller; the RESOURCE has to compare it and refuse the
// lower one, and most resources — a file, an HTTP endpoint, a table without a
// version column — cannot. Where the fence is not checked, mutual exclusion is
// not guaranteed against a stalled holder: a long GC pause, a SIGSTOP, or a
// descheduled thread can put two holders inside one section, and neither will
// observe it.
//
// That is a limitation, stated here rather than buried, because a reader who
// assumes otherwise will build on a guarantee that does not exist. If the
// protected resource cannot check a fence, do not rely on a lease TTL for
// correctness — use a backend whose lock does not expire at all
// (internal/service/lock's file store is one; see its CLAUDE.md) and accept a
// hung holder blocking its waiters, which is a liveness failure you can see
// instead of a safety failure you cannot.
//
// # There is no registry
//
// Like proc (ADR 0016), resilience (ADR 0026), net (ADR 0029), scheduler
// (ADR 0041), token (ADR 0042), session (ADR 0045), validation (ADR 0046) and
// cache (ADR 0049), this domain has no name-to-implementation registry.
//
// The reason is specific to locking and it is not taste. The two backends
// differ in the one property a caller must know — whether a lease can be
// taken away from a live holder — and resolving one of them from a
// configuration string would let a typo swap a lock that never expires for a
// lock that does, silently, with no failure at the moment of the swap. Every
// call would still succeed. The difference would surface only as the rarest
// class of production bug there is.
//
// # Distributed locks are out of scope
//
// A lock over Redis, etcd, ZooKeeper or Consul is a connector to a third-party
// system and belongs under third-party/, not here. This domain covers exactly
// two scopes: one process (goroutines) and one machine (processes sharing a
// filesystem). It never implies a third.
package lock

import "context"

// Locker hands out named, exclusive leases. Implementations MUST be safe for
// concurrent use by multiple goroutines.
//
// The method set is deliberately two operations wide and is FROZEN: pkg/v1
// aliases this interface, so under ADR 0039 a third method would break every
// downstream implementer at compile time with no deprecation window. New
// capabilities arrive as SIBLING interfaces discovered by type assertion —
// [Deadliner] on the lease is the one that ships, and the model is codec's
// Appender (ADR 0037).
//
// There is no TTL parameter on either method, and its absence is the design.
// A lease lifetime belongs to the WORK being protected, which is known where a
// Locker is wired, not at the call site — and a per-call duration would create
// exactly one place where a zero can be typed by accident, in a hot path,
// under time pressure, and be read as a lock that has already expired. The TTL
// lives in the implementation's configuration and is refused there if it is
// not positive (ADR 0031, ADR 0052).
//
// IFACE-PLUGIN: the concrete lockers stay unexported behind their constructors
// in internal/service/lock.
type Locker interface {
	// Acquire blocks until the named lock is held by this caller or ctx ends.
	// On success it returns a [Lease] that MUST eventually be released.
	//
	// A cancelled or expired ctx returns the context's own error, not an SDK
	// sentinel: the caller supplied the deadline and already knows what it
	// means. An empty name is refused with [LockNameRejected].
	Acquire(ctx context.Context, name string) (Lease, error)
	// TryAcquire attempts the acquisition once and never waits for another
	// holder. It reports (lease, true, nil) on success and (nil, false, nil)
	// when the lock is held elsewhere.
	//
	// "Held elsewhere" is NOT an error: a caller that offered to give up
	// immediately has had its offer accepted, and nothing went wrong. An error
	// return means the backend could not answer the question at all.
	TryAcquire(ctx context.Context, name string) (Lease, bool, error)
}
