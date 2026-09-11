//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/lock .

// Package lock is the public facade for the SDK's mutual exclusion: named,
// exclusive, fenced leases over one process or over one machine.
//
//	locker, err := lock.NewMemory(lock.MemoryConfig{TTL: 30 * time.Second})
//	if err != nil { return err }
//
//	lease, err := locker.Acquire(ctx, "rebuild:index")
//	if err != nil { return err }
//	defer lease.Release(ctx)
//
// # Read this before relying on a lease
//
// A lock that reports success it cannot back is worse than no lock at all: a
// caller who believes they hold a critical section behaves differently from
// one who knows they do not, and a lie removes the second behaviour without
// putting anything in its place. Three consequences shape this whole package.
//
// FIRST — a zero TTL is REFUSED at construction. It is not read as "expires
// immediately" and not as "never expires". Both are defensible, they are
// opposites, and a locker built on the first grants every Acquire while
// excluding nobody — indistinguishable, from outside, from a lock nobody
// contends (ADR 0031).
//
// SECOND — expiring a lease does not stop its old holder. If A's goroutine
// stalls for longer than the TTL and B acquires the lock, A resumes with no
// idea anything happened. So [Lease.Release] releases only a lock this holder
// STILL holds — a lease that was taken over releases nothing and reports
// [LockNotHeld] — and [Lease.Extend] renews a lease for work that outlives its
// TTL. A failed Extend is not a warning to log past: it is the SDK telling a
// caller it is already inside someone else's section.
//
// THIRD — every acquisition carries a FENCING TOKEN. [Lease.Fence] returns a
// uint64 that strictly increases with every acquisition of that name. Pass it
// to the protected resource and have the resource refuse the lower one, and
// the scenario above turns from silent corruption into a rejected write.
//
// # What this package does not guarantee
//
// Issuing a fencing token is not enforcing one. The SDK can hand you the
// number; only the RESOURCE can compare it, and many cannot — a plain file, an
// HTTP endpoint, a table with no version column. Where the fence is not
// checked, mutual exclusion is NOT guaranteed against a stalled holder: a long
// GC pause, a SIGSTOP or a descheduled thread can put two holders inside one
// section, and neither will notice.
//
// If the protected resource cannot check a fence, do not rely on a TTL for
// correctness. Use [NewFileLocker], whose leases never expire, and accept that
// a hung holder blocks its waiters — a liveness failure you can see, instead
// of a safety failure you cannot.
//
// # Two backends, and the difference is not performance
//
//   - [NewMemory] excludes the GOROUTINES of one process. Its leases expire,
//     because nothing notices a goroutine that stopped, so without a TTL one
//     leak deadlocks a name for the life of the process. Its leases implement
//     [Deadliner].
//
//   - [NewFileLocker] excludes the PROCESSES sharing one directory on one
//     machine, through flock(2). Its leases do NOT expire: a lock is held
//     until it is released or until the holder's process dies, at which point
//     the kernel releases it. Nothing can take it from a live holder, so its
//     leases do NOT implement [Deadliner] — and that absence is how you find
//     out, from the API rather than from this paragraph.
//
// Which world you are in is one type assertion away:
//
//	if d, ok := lease.(lock.Deadliner); ok {
//	    // this lease CAN be taken from you; renew it or carry the fence
//	}
//
// The file locker takes an in-process gate before its flock, because flock(2)
// is per open file description: re-locking a description that already holds it
// is a no-op, so a shared descriptor gives ZERO exclusion between goroutines
// while working perfectly between processes. That was measured, not assumed —
// see internal/service/lock's CLAUDE.md.
//
// # Scope
//
// One process, or one machine. There is deliberately no distributed backend
// here: a lock over Redis, etcd, ZooKeeper or Consul is a connector to a
// third-party system and belongs under third-party/. Nothing in this package
// implies coordination beyond the filesystem it was given.
package lock

import (
	"context"

	corelock "github.com/kitsunium/sdk/internal/core/lock"
	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// Locker hands out named, exclusive leases: Acquire and TryAcquire.
//
// The method set is FROZEN at two. New capabilities arrive as SIBLING
// interfaces you reach by type assertion, because this alias publishes the
// interface, Go interfaces are structural, and a third method would break
// every downstream implementer at compile time with no deprecation window
// (ADR 0039).
type Locker = corelock.Locker

// Lease is a held lock: Fence, Extend, Release. FROZEN at three, for the same
// reason [Locker] is frozen at two.
type Lease = corelock.Lease

// Deadliner is the sibling implemented by a [Lease] that CAN expire — i.e. one
// that can be taken from you while you are still running.
//
// Its presence is the only question that changes how a caller must be written.
// A memory lease implements it; a file lease does not, because a file lock has
// no deadline at all. It is deliberately not a Deadline method on [Lease]
// returning a zero time for "never": a zero time.Time is a value someone will
// compare against time.Now and lose to, and an absent interface is not.
type Deadliner = corelock.Deadliner

// MemoryConfig parameterises [NewMemory]. TTL must be positive.
type MemoryConfig = svclock.MemoryConfig

// FileConfig parameterises [NewFileLocker]. Dir must be set; Poll defaults.
type FileConfig = svclock.FileConfig

// KeepaliveConfig parameterises [Keepalive]. Every must be positive and should
// be comfortably shorter than the lease TTL — a third of it leaves room for
// two consecutive failed renewals.
type KeepaliveConfig = svclock.KeepaliveConfig

// The sentinels a caller matches with errors.Is or errs.HasCode.
var (
	// LockMisconfigured is returned by a constructor whose configuration
	// cannot be honoured — most importantly a TTL that is not positive.
	LockMisconfigured = corelock.LockMisconfigured

	// LockNotHeld is returned by Extend or Release when this caller's lease
	// has been taken over. From Extend it means you are inside a section
	// someone else owns; stop the protected work.
	LockNotHeld = corelock.LockNotHeld

	// LockBackendFailed is returned when the locker could not serve the
	// operation at all. A lock merely being HELD is not this — TryAcquire
	// reports that as (nil, false, nil).
	LockBackendFailed = corelock.LockBackendFailed

	// LockNameRejected is returned for an empty or unusable lock name.
	LockNameRejected = corelock.LockNameRejected

	// LockFenceCorrupt is returned by the file locker when the on-disk fencing
	// ledger is not a decimal counter. Nothing is repaired: a restarted
	// counter reissues numbers the protected resource has already accepted.
	LockFenceCorrupt = svclock.LockFenceCorrupt

	// LockDirectoryUnsafe is returned by [NewFileLocker] for a world-writable,
	// non-sticky directory, whose lock files any account could replace with a
	// fresh inode — splitting one lock into two.
	LockDirectoryUnsafe = svclock.LockDirectoryUnsafe

	// LockKeepaliveLost is the context CAUSE published by [Keepalive] when a
	// renewal fails.
	LockKeepaliveLost = svclock.LockKeepaliveLost
)

// NewMemory returns a [Locker] whose leases live in this process and DO
// expire.
//
// cfg.TTL must be positive; a zero or negative TTL is refused here rather than
// defaulted, because the two natural readings of zero are opposites and either
// choice would be silently wrong for half of its callers (ADR 0031).
func NewMemory(cfg MemoryConfig) (locker Locker, err error) {
	//: delegate to the service constructor, which validates and builds.
	return svclock.NewMemory(cfg)
}

// NewFileLocker returns a [Locker] that excludes every process using the same
// directory on the same machine, and whose leases do NOT expire.
//
// It refuses at construction: a missing directory setting, a negative poll
// interval, a world-writable non-sticky directory, and a platform without
// flock(2) — where it returns the SDK-wide UnsupportedPlatform rather than a
// locker that would report success and exclude nothing (ADR 0018).
func NewFileLocker(cfg FileConfig) (locker Locker, err error) {
	//: delegate to the service constructor, which validates and builds.
	return svclock.NewFileLocker(cfg)
}

// Keepalive renews lease in the background and returns a context that is
// CANCELLED the instant the lease stops being held, plus the function that
// ends the renewal.
//
// The loss arrives as a cancellation because [Lease.Extend] returning an error
// only helps a caller currently calling it — and the caller that needs the
// news is the one already inside the critical section. context.Cause names it.
//
//	guarded, stop, err := lock.Keepalive(ctx, lease, lock.KeepaliveConfig{Every: ttl / 3})
//	if err != nil { return err }
//	defer stop()
//	// hand `guarded` to the protected work; it ends if the lease is lost
//
// stop does NOT release the lease: the lifetime of a lock must not depend on
// the lifetime of a convenience.
func Keepalive(ctx context.Context, lease Lease, cfg KeepaliveConfig) (guarded context.Context, stop context.CancelFunc, err error) {
	//: delegate to the service helper, which validates and starts the loop.
	return svclock.Keepalive(ctx, lease, cfg)
}
