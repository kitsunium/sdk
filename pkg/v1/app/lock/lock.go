//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/app/lock .

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
// idea anything happened. So [Lease].Release releases only a lock this holder
// STILL holds — a lease that was taken over releases nothing and reports
// [LockNotHeld] — and [Lease].Extend renews a lease for work that outlives its
// TTL. A failed Extend is not a warning to log past: it is the SDK telling a
// caller it is already inside someone else's section.
//
// THIRD — every acquisition carries a FENCING TOKEN. [Lease].Fence returns a
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
//     machine, through flock(2) on Unix and LockFileEx on Windows. Its leases
//     do NOT expire: a lock is held until it is released or until the holder's
//     process dies, at which point the kernel releases it. Nothing can take it
//     from a live holder, so its leases do NOT implement [Deadliner] — and
//     that absence is how you find out, from the API rather than from this
//     paragraph.
//
// Which world you are in is one type assertion away:
//
//	if d, ok := lease.(lock.Deadliner); ok {
//	    // this lease CAN be taken from you; renew it or carry the fence
//	}
//
// The file locker takes an in-process gate before its kernel lock, because
// flock(2) is per open file description: re-locking a description that already
// holds it is a no-op, so a shared descriptor gives ZERO exclusion between
// goroutines while working perfectly between processes. That was measured, not
// assumed — see internal/service/app/lock's CLAUDE.md.
//
// # What differs on Windows, and what does not
//
// The contract does not differ: same Locker, same Lease, same sentinels, and a
// file lease still does not implement [Deadliner]. The primitive underneath
// does. LockFileEx locks a byte RANGE rather than a file — this backend takes
// the whole file, because the fencing counter lives in it — and its locks are
// MANDATORY rather than advisory, so one thing a caller can observe changes:
// while a lock is HELD, a process that does not hold it cannot read the lock
// file. On Unix it can. Every other difference is absorbed below the port and
// measured on a real Windows kernel; ADR 0081 lists them.
//
// One more is worth knowing before you rely on a directory's permissions. The
// lock directory is checked for being one any account can REPLACE an entry in,
// and the two platforms answer that in their own vocabulary: a
// world-writable-and-not-sticky mode on Unix, and on Windows a discretionary
// entry letting Everyone, Authenticated Users or BUILTIN\Users unlink somebody
// else's entry — or write the lock files created there, which is a question
// Unix never has to ask, because a lock file there is created 0600 whatever
// the directory's mode says (ADR 0084, ADR 0086).
//
// Windows does have the sticky directory's shape; it spells it in two bits
// rather than one, as "may add an entry" without "may delete a child".
// %ProgramData% is one, which is why a lock directory under it is accepted and
// a junction planted beside it is not.
//
// # A lock path is a file, never a link to one
//
// [NewFileLocker] names its lock files hex(sha256(lockName)) + ".lock" inside
// the directory you gave it. That keeps every caller-supplied string off the
// filesystem and, in the same stroke, makes the name PREDICTABLE — and an indirection planted at a predictable name is
// enough to move your lock somewhere you did not choose.
//
// So a symbolic link (Unix) or a reparse point (Windows) at the lock path is
// REFUSED with [LockPathRedirected]. It is not followed, and it is not read as
// a backend failure, because nothing failed: the substitution worked, and a
// retry is the one response that would make it worse.
//
// This is a REFUSAL WHERE THERE USED TO BE SUCCESS. If your deployment
// deliberately symlinks a lock file — onto a tmpfs, say — it now fails at
// Acquire. There is no flag to restore the old behaviour: it would be a flag
// to restore a lock that lands somewhere the locker did not report (ADR 0082).
//
// The PARENT components are governed too, and by a different rule, because a
// symbolic link at a parent is not evidence of anything on its own: /tmp is
// one on macOS and /var/run is one on most Linux distributions. An
// indirection above the lock file is refused only when the directory holding
// it is world-writable — when anybody could have planted it. On Windows the
// same rule runs over the same question, answered by the directory's DACL
// rather than by a synthesised mode (ADR 0083, ADR 0084) — and it asks for a
// different right from the lock directory's own rule, because a component is
// a DIRECTORY, so what plants one is "may add a subdirectory" (ADR 0086).
//
// # A held lock can lose its file, and you are told
//
// [LockFileReplaced] is the one exposure this package DETECTS rather than
// prevents. See its own documentation; the short version is that Extend is
// where you find out, and finding out is the whole of what is offered.
//
// # Scope
//
// One process, or one machine. There is deliberately no distributed backend
// here: a lock over Redis, etcd, ZooKeeper or Consul is a connector to a
// third-party system and belongs under third-party/. Nothing in this package
// implies coordination beyond the filesystem it was given.
package lock
