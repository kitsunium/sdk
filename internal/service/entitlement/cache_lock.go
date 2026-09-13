// Package entitlement - exclusion over the cache directory, which is the one
// thing write-then-rename does not provide.
//
// Renaming a staged file over an installed one is atomic for a READER: nobody
// ever observes half a bundle at the name they look for. It says nothing about
// two WRITERS, and the ratchet is not a write — it is a read, a comparison and
// a write, and the comparison is only worth anything if nothing moves between
// it and the write it guards.
//
// The exclusion is also what lets a refresh land on Windows. There, replacing
// a file another handle holds open is refused, and every file Go opens is held
// that way: syscall.Open asks for FILE_SHARE_READ|FILE_SHARE_WRITE and never
// FILE_SHARE_DELETE. Keeping readers and the writer off the bundle at the same
// time removes the collision rather than retrying past it. Sharing DELETE from
// the read side is NOT an alternative: measured on windows-latest, MoveFileEx
// still refuses a destination held with FILE_SHARE_DELETE — see
// Test_windowsRenameOverAnOpenDestination, which asserts all three share modes
// on a real kernel, and ADR 0079's Deferred section for what is still open.
package entitlement

import (
	"context"
	"log"
	"time"

	corelock "github.com/kitsunium/sdk/internal/core/lock"
	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// cacheLockName is the lock every holder of one cache directory takes.
//
// The name is scoped by the DIRECTORY — a file locker's locks exclude only
// other users of the same directory — so one constant serves every product.
// Two products with separate cache directories do not contend, and two
// processes sharing one do, which is exactly the grouping wanted.
const cacheLockName string = "roster"

// cacheLockBudget bounds how long a caller waits for the cache.
//
// The protected section is a file read, an ed25519 verification and a rename:
// microseconds, with no network and no user interaction inside it. A wait that
// reaches seconds therefore means something pathological — a holder stopped in
// a debugger, a filesystem that has stopped answering — and the right response
// to that is to carry on without the exclusion rather than to hang a licence
// gate that a shell is waiting on.
const cacheLockBudget time.Duration = 2 * time.Second

// cacheGuard returns the locker guarding this cache directory, or nil when
// there is none to be had.
//
// Built on first use rather than in a constructor, because there are three
// ways a Service acquires a cache directory — NewService reads it from the
// product, WithCache sets it, and this package's own tests write the field —
// and a guard that only one of them installed would be a guard that production
// or the tests silently went without.
//
// It CREATES the cache directory, which the first successful write used to do.
// On Windows, where reads are guarded too, a machine that never manages to
// fetch a roster therefore ends up with an empty 0700 directory holding one
// `.lock` file, where before it had nothing. That is a visible change and it is
// accepted: the alternative is to take the guard only once there is something
// to guard, which is exactly the ordering that makes a first write race a
// concurrent one.
//
// nil is a real answer, not a failure to report. A platform with no file-range
// lock (NewFileLocker refuses with coreproc.UnsupportedPlatform) and a cache
// directory whose lock files any account could replace both yield one, and in
// both cases the cache must keep working: the offline grant is what a machine
// falls back on, so degrading it to "no cache" to protect a ratchet would trade
// a narrow race for a certain outage. What is lost without a guard is stated
// where it is lost, in holdCache.
func (s *Service) cacheGuard() corelock.Locker {
	//: One attempt per Service. A locker that could not be built cannot be
	//: built by trying again on every roster fetch, and retrying would repeat
	//: the log line below on every one of them.
	s.cacheLockOnce.Do(func() {
		//: No cache means nothing to exclude anyone from.
		if s.cacheDir == "" {
			//: Leave the locker nil.
			return
		}
		locker, lockErr := svclock.NewFileLocker(svclock.FileConfig{Dir: s.cacheDir})
		//: Say it once, and say what is lost by it — silence here turns
		//: "the ratchet is not serialised on this machine" into something
		//: nobody can discover.
		if lockErr != nil {
			log.Printf("cannot guard the roster cache at %s (%v); concurrent refreshes on this machine are not serialised", s.cacheDir, lockErr)
			//: Leave the locker nil.
			return
		}
		s.cacheLock = locker
	})
	//: Whatever the single attempt produced.
	return s.cacheLock
}

// holdCache runs fn with the cache directory held against every other
// goroutine in this process and every other process on this machine.
//
// It runs fn either way. Both the no-guard and the timed-out paths fall
// through to an UNGUARDED call, and that is a deliberate choice with a price:
// it is exactly the behaviour this package had before the guard existed, so a
// machine that cannot take the lock is no worse off than it was, while a
// machine that refused to read its cache without one would lose the offline
// grant over a contended lock file. What it costs is that the ratchet's
// compare-and-install is not atomic on that machine, and the log line above
// is the only warning of it.
func (s *Service) holdCache(fn func()) {
	locker := s.cacheGuard()
	//: No guard: run unguarded rather than not at all.
	if locker == nil {
		fn()
		//: Done, without exclusion.
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), cacheLockBudget)
	//: Released at the point it was acquired.
	defer cancel()

	lease, acquireErr := locker.Acquire(ctx, cacheLockName)
	//: A lock we cannot take is not a reason to skip the work; see above.
	if acquireErr != nil {
		log.Printf("cannot hold the roster cache at %s (%v); proceeding without exclusion", s.cacheDir, acquireErr)
		fn()
		//: Done, without exclusion.
		return
	}
	//: Released at the point it was acquired, on every path out of fn —
	//: including a panic, because a lock file left held by a process that
	//: survived would exclude this one from its own cache until it exits.
	defer func() {
		//: Best-effort: a lease we cannot release is released by the kernel
		//: when this process ends, so it is worth a line and never a refusal.
		if releaseErr := lease.Release(context.Background()); releaseErr != nil {
			log.Printf("releasing the roster cache lock at %s: %v", s.cacheDir, releaseErr)
		}
	}()

	fn()
}
