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
// to that is to stop waiting rather than to hang a licence gate a shell is
// sitting in front of.
const cacheLockBudget time.Duration = 2 * time.Second

// cacheLockPoll is how often a waiting caller retries while another process
// holds the cache.
//
// The locker's own default is 25 ms, chosen for a section whose length it
// cannot know. This one's length IS known — a file read, an ed25519
// verification and a rename — so 25 ms is roughly a thousand times the wait it
// is pacing, and every contended handover pays most of it as dead time. One
// millisecond is still four or five wakeups across a section that long, which
// is polling rather than spinning.
//
// Measured on the contended probe, 400 rounds of two writers racing:
//
//	25 ms (the locker's default):  11.66 s
//	 1 ms (this value):              2.53 s
const cacheLockPoll time.Duration = time.Millisecond

// cacheGuard returns the locker guarding this cache directory, or nil when
// there is none to be had.
//
// Built on first use rather than in a constructor, because there are three
// ways a Service acquires a cache directory — NewService reads it from the
// product, WithCache sets it, and this package's own tests write the field —
// and a guard that only one of them installed would be a guard that production
// or the tests silently went without.
//
// It is built once PER DIRECTORY, not once per Service. A sync.Once would have
// been simpler and would have been wrong: WithCache can retarget a Service
// after it has already cached something, and a locker retained from the first
// directory would then exclude holders of a directory nobody is using while
// leaving the live one unguarded — a failure that looks exactly like success.
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
// a narrow race for a certain outage.
func (s *Service) cacheGuard() corelock.Locker {
	//: No cache means nothing to exclude anyone from, and nothing worth
	//: remembering about it either.
	if s.cacheDir == "" {
		//: No guard.
		return nil
	}

	s.cacheLockMu.Lock()
	//: Released at the point it was acquired.
	defer s.cacheLockMu.Unlock()

	//: Already answered for THIS directory — including answered "nil", which
	//: must not be retried on every roster fetch: a locker that could not be
	//: built cannot be built by asking again, and asking again would repeat
	//: the log line below every time.
	if s.cacheLockFor == s.cacheDir {
		//: Whatever the one attempt for this directory produced.
		return s.cacheLock
	}

	//: Record the attempt BEFORE making it, so a failure is remembered as
	//: firmly as a success and a retargeted Service never keeps the old
	//: directory's locker.
	s.cacheLockFor = s.cacheDir
	s.cacheLock = nil

	locker, lockErr := svclock.NewFileLocker(svclock.FileConfig{Dir: s.cacheDir, Poll: cacheLockPoll})
	//: Say it once, and say what is lost by it — silence here turns "the
	//: ratchet is not serialised on this machine" into something nobody can
	//: discover.
	if lockErr != nil {
		log.Printf("cannot guard the roster cache at %s (%v); concurrent refreshes on this machine are not serialised", s.cacheDir, lockErr)
		//: No guard.
		return nil
	}
	s.cacheLock = locker
	//: Kept for as long as this Service points at this directory.
	return s.cacheLock
}

// underCacheLock runs fn with the cache directory held against every other
// goroutine in this process and every other process on this machine, and
// reports whether fn RAN.
//
// It returns false in exactly one situation: a guard exists and the lock could
// not be taken within the budget. It does NOT decide what that means, because
// the two callers want opposite things from it — a write must not proceed
// unguarded, a read must not be abandoned — and a helper that chose for both
// would have to be wrong for one of them.
//
// A nil guard is NOT that situation. There, fn runs unguarded and this reports
// true, because "this platform has no file lock" is a standing fact about the
// machine rather than a passing state of the cache, and treating it as
// contention would disable caching on such a machine permanently.
func (s *Service) underCacheLock(fn func()) (ran bool) {
	locker := s.cacheGuard()
	//: No exclusion to be had here at all: run unguarded, exactly as this
	//: package did before the guard existed.
	if locker == nil {
		fn()
		//: Ran, without exclusion.
		return true
	}

	ctx, cancel := context.WithTimeout(context.Background(), cacheLockBudget)
	//: Released at the point it was acquired.
	defer cancel()

	lease, acquireErr := locker.Acquire(ctx, cacheLockName)
	//: Somebody else is inside the section right now, or the backend cannot
	//: answer at all. Either way fn has not run, and the caller decides what
	//: that is worth.
	if acquireErr != nil {
		//: Did not run.
		return false
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
	//: Ran, under exclusion.
	return true
}

// holdCacheForWrite runs a cache WRITE under exclusion, and SKIPS it when
// exclusion is available but cannot be taken.
//
// Skipping is the point, and it is the half a first version of this file got
// wrong by falling through. This lock is taken by exactly one thing, so failing
// to get it means another holder is inside the compare-and-install at this
// instant — doing the same work, with the same roster, from the same origins.
// Proceeding without it would be the lost update this file exists to remove,
// reintroduced on the one path allowed to lower the mark. Nothing is lost by
// standing down: the holder is installing, and the next verification caches
// whatever it fetches.
func (s *Service) holdCacheForWrite(fn func()) {
	//: Ran — guarded, or unguarded on a machine with no guard to be had.
	if s.underCacheLock(fn) {
		//: Installed, or deliberately not, by fn's own judgement.
		return
	}
	//: Name which of the two silences this is: "the cache did not change"
	//: means something quite different here and in cacheGuard's log line.
	log.Printf("roster cache at %s is held elsewhere; leaving this refresh to the holder", s.cacheDir)
}
