# ADR 0052 — mutual-exclusion domain (`lock`): ownership, renewal and fencing decided out loud, and the guarantee that is NOT made

- **Status**: Accepted
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Related**: [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a policy's zero value is a safe default or an explicit refusal — the rule this domain is shaped by), [ADR 0018](0018-sdk-cross-platform-portability.md) (build bar + runtime bar, uniform `UnsupportedPlatform`), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published port grows by siblings), [ADR 0025](0025-sdk-cache-kernel.md) (the `clock` primitive every TTL here is asserted through), [ADR 0045](0045-sdk-session-domain.md) (the `flock` + platform-refusal precedent this follows), [ADR 0041](0041-sdk-scheduler-domain.md) (the no-wall-clock-wait AST audit this reuses)

## Context

The SDK has fifteen core domains and none of them can say "only one of you at a
time". Every consumer that needs it writes it again: a `sync.Mutex` where the
scope is one process, a `.lock` file and a hand-rolled `flock` where it is one
machine, and — usually — a TTL bolted on afterwards because the first version
deadlocked when a holder died.

That last step is where the hand-rolled version stops being a lock. Adding a
TTL turns "held until released" into "held until released **or** until a timer
says otherwise", and the timer does not tell the holder. The result reports
success on every call and excludes nobody at the exact moment it matters. This
is the same failure ADR 0031 was written for — a circuit breaker that was
constructed fail-open and gave a false sense of protection — and a lock is its
sharpest case: a caller who believes they hold a critical section behaves
differently from one who knows they do not, so a lock that lies removes the
second behaviour and puts nothing in its place.

Two mechanics also make this a domain the SDK can implement well and a
consumer usually cannot:

- **`clock.Timed`** (ADR 0025/0039) means every TTL claim here is asserted by
  jumping the clock rather than by sleeping past a deadline. A suite that
  sleeps to test expiry is a suite whose meaning is its timing.
- **`flock(2)` diverges hard across platforms** — and, as measured below,
  diverges from what almost everyone believes it does *within one process*.

## Decision

Add `lock` as the **19th core sibling**: `internal/core/lock` (`0.2.21.*`),
`internal/service/lock` (`0.3.51.*`), `pkg/v1/lock`. Two implementations ship —
in-process and file-backed. **No registry.**

### D1 — The port, frozen, with no TTL parameter

```go
type Locker interface {
	Acquire(ctx context.Context, name string) (Lease, error)
	TryAcquire(ctx context.Context, name string) (Lease, bool, error)
}

type Lease interface {
	Fence() uint64
	Extend(ctx context.Context) error
	Release(ctx context.Context) error
}

type Deadliner interface { Deadline() time.Time } // sibling, ADR 0039
```

`Locker` is frozen at two methods and `Lease` at three; both are aliased by
`pkg/v1`, so under ADR 0039 a further method breaks every downstream double at
compile time with no deprecation window. Both freezes are guarded by named
tests (`TestTwoMethodDoubleStillSatisfiesLocker`,
`TestThreeMethodDoubleStillSatisfiesLease`).

**Neither method takes a TTL, and the absence is the design.** A lease lifetime
belongs to the *work* being protected, which is known where a `Locker` is
wired — not at the call site. A per-call duration would create exactly one
place where a zero can be typed by accident, in a hot path, under time
pressure, and be read as a lock that has already expired. The TTL lives in the
constructor's configuration, where D2 refuses it.

`TryAcquire` reporting "held elsewhere" is `(nil, false, nil)` and **not an
error**: a caller who offered to give up immediately has had its offer
accepted, and nothing went wrong.

### D2 — A zero TTL is refused at CONSTRUCTION, never interpreted

`MemoryConfig.TTL` must be positive. Zero and negative are refused with
`LOCK_MISCONFIGURED` (`0.2.21.1`) before anything is allocated.

This is ADR 0031's *refuse* half, and the reasoning is stronger here than for
any policy it was originally written about. A zero TTL has two natural
readings — "already expired" and "never expires" — and they are **opposites**.
Whichever the SDK picked would be silently wrong for half its callers:

- Read as *already expired*, every `Acquire` succeeds and the locker excludes
  nobody. From outside it is indistinguishable from a lock nobody contends.
- Read as *never expires*, one leaked goroutine deadlocks a name for the life
  of the process.

There is no third value to clamp to, so there is no default. Refusing at
**construction** rather than at the first `Acquire` is the other half: the
failure lands in the process's first second, where the program is wired, not
in the middle of the first contended section.

The same struct carries the contrast deliberately. `FileConfig.Poll` — the
retry interval — is **clamped** to 25 ms when zero, because a poll interval has
one obviously-right order of magnitude and no caller's correctness depends on
the exact value. Two ADR 0031 halves, one file, so the difference is legible.

### D3 — Ownership: a lease releases only what its holder still holds

Every acquisition carries an unexported holder **token**. `Release` compares it
under the locker's mutex:

- token still current → release, `nil`.
- token superseded (the lease lapsed and someone else took the lock) → release
  **nothing**, return `LOCK_NOT_HELD` (`0.2.21.2`).
- already released by this holder → `nil`, idempotent.

The middle case is the whole decision. The obvious implementation — unlock by
*name* — has the lapsed holder politely ending the critical section the new
holder is inside, and the new holder never learns of it. Refusing costs a
leaked lock until the TTL; unlocking costs a correctness violation nobody
observes. `TestTheOldHolderCannotReleaseAfterTakeover` asserts both halves: the
stale `Release` reports `LOCK_NOT_HELD` **and** the current holder still holds
the lock.

`Release` deliberately **ignores its context**. The overwhelmingly common call
site is `defer lease.Release(ctx)` with the very context whose cancellation
ended the work; honouring it would mean every timed-out operation leaks its
lease for a full TTL, and the leak would look like contention rather than like
a bug. For the file locker the consequence would be worse — the `flock` would
be held until process exit, blocking other processes too.

### D4 — Renewal: `Extend`, and a keepalive that cancels rather than logs

`Lease.Extend` renews for a full lifetime **from now**, not from the old
deadline: renewal is about the work still ahead, and stacking from the past
would shrink every renewal after a slow one.

Its sharpest edge: **`Extend` on a lapsed lease is refused even when nobody has
taken the lock yet.** Expiry is the deadline, not "until somebody else takes
it". Reporting success in that window would reassure a holder for exactly the
period in which any other caller was entitled to take the lock — the moment a
holder most needs the truth.

A mechanism nobody remembers to call is not a guarantee, so
`service/lock.Keepalive` ships alongside it:

```go
guarded, stop, err := lock.Keepalive(ctx, lease, lock.KeepaliveConfig{Every: ttl / 3})
defer stop()
```

It renews on the injected clock and, when a renewal fails, **cancels the
derived context** with `LOCK_KEEPALIVE_LOST` (`0.3.51.3`) as its cause. The
cancellation is the point: `Extend` returning an error only helps a caller
currently calling it, while the caller who needs the news is the one already
inside the section — and the only channel that reaches work in progress is its
context.

This is deliberately the **opposite** of ADR 0043's drain signal. A drain is an
invitation to finish, so cancelling would be wrong. Losing a lock is not an
invitation: continuing means writing into a section another holder believes it
owns. `stop()` does not release the lease — the lifetime of a lock must not
depend on the lifetime of a convenience. A `nil` lease is refused rather than
renewed, because a keepalive over nothing reports healthy forever.

### D5 — Fencing IS shipped, and its limit is stated as loudly

`Lease.Fence() uint64` strictly increases with every acquisition of a name,
takeover and clean handover alike — a resource cannot tell the two apart, so
both must order. It is never 0 for a live lease, so a caller who forgets to
plumb it through cannot have it compare equal to a legitimate token.

- **In-process**: a per-name counter in a ledger that is **never pruned**.
  Forgetting a name's counter resets it, and a reset fencing token reissues
  numbers the protected resource has already accepted. The cost is one `uint64`
  per distinct name for the process's lifetime, recorded as a bound on the
  names a caller should mint.
- **File-backed**: the counter lives **in the lock file**, read-modify-written
  under the very `flock` being taken, and `fsync`ed before the lease is handed
  out. It therefore survives restarts and reboots. A ledger that is not a
  decimal counter is **refused** (`LOCK_FENCE_CORRUPT`, `0.3.51.1`), never
  repaired: "repairing" means restarting the counter, and a restarted fence is
  not a fence.

**What the domain does not guarantee.** Issuing a fencing token is not
enforcing one. The SDK can hand the caller a number; only the **resource** can
compare it and refuse the lower one, and many cannot — a plain file, an HTTP
endpoint, a table with no version column. Where the fence is not checked,
**mutual exclusion is not guaranteed against a stalled holder**: a long GC
pause, a `SIGSTOP` or a descheduled thread can put two holders inside one
section and neither will observe it.

That sentence is in the core package comment, in the `pkg/v1` doc comment, in
both `CLAUDE.md` files and here, because a reader who assumes otherwise builds
on a guarantee that does not exist. The honest advice follows from it: if the
protected resource cannot check a fence, do not rely on a TTL for correctness —
use the file locker, whose leases do not expire.

### D6 — The two backends expire differently, and the API says which

| | `NewMemory` | `NewFileLocker` |
|---|---|---|
| Excludes | goroutines of one process | processes sharing one directory on one machine |
| Lease expires | **yes**, after `TTL` | **no** |
| Taken from a live holder | yes | never |
| Implements `Deadliner` | **yes** | **no** |
| Fence ledger | in-memory, per name | on disk, survives reboot |

The asymmetry comes from a real difference, not a preference. **A dead process
is noticed by the kernel**, immediately and reliably: `flock` is released when
the last descriptor referring to the open file description is closed, which
process death does. The failure a TTL exists to recover from is therefore
already handled for the file locker, and what a TTL would *add* is the ability
to take the lock from a process that is still alive — the one thing D5 says
cannot be made safe for a resource that ignores fences.

**A dead goroutine is noticed by nobody.** It leaves no trace the runtime acts
on and nothing releases its lock, so without expiry one leak deadlocks a name
for the life of the process. Hence: the in-process locker expires, the file
locker does not.

The price of not expiring is stated rather than hidden: a holder that **hangs
without dying** blocks its waiters indefinitely. That is a liveness failure, it
is visible — every waiter is blocked in `Acquire` on a context whose deadline
it chose — and it is strictly preferable to a safety failure in which two
processes are inside one section, neither blocked, neither logging, the
evidence arriving much later as corrupted data.

**How a caller discovers which world it is in:** `Deadliner` (ADR 0039). A
memory lease implements it, a file lease does not. `TestFileLeaseIsNotADeadliner`
is the negative guard, because if a file lease ever grew a `Deadline` method
nothing would fail to compile and nothing would fail at run time — callers
would simply start renewing and fencing a lease that needs neither.

This is deliberately not a `Deadline()` on `Lease` returning the zero time for
"never": a zero `time.Time` is a value someone will compare against `time.Now`
and lose to. An absent interface is not.

### D7 — `flock(2)` gives ZERO exclusion between goroutines — measured, not assumed

The file locker takes an **in-process gate before the `flock`**. The reason was
measured on this machine (linux/amd64, kernel 6.12), before the backend was
written, because the received wisdom is wrong in a way that only shows up under
goroutine concurrency:

| Case | Result |
|---|---|
| Two separate `open(2)` calls, same process, second `LOCK_EX\|LOCK_NB` | `EWOULDBLOCK` — **they do exclude** |
| Two separate descriptions, blocking `LOCK_EX` | blocked ≥ 300 ms — they do exclude |
| **Same description re-locked** (`LOCK_EX` on a descriptor already holding it) | **succeeds immediately** — a lock *conversion*, not a wait |
| 8 goroutines sharing one descriptor around a counted critical section | **max occupancy 8 of 8**, every run |
| `dup(2)`'d descriptor | shares the description; re-lock succeeds, closing the dup does not release |
| Another process, `LOCK_EX\|LOCK_NB` while held | `EWOULDBLOCK` — inter-process exclusion works |

The fourth row is decisive. Holding **one descriptor for the store's lifetime**
is the natural, efficient design, and it makes `flock` a *complete no-op*
between goroutines while continuing to work perfectly between processes. The
defect is invisible to any test that spawns processes and appears only under
goroutine concurrency — the opposite of where anyone looks for a file-locking
bug.

So the locker composes: the gate guarantees goroutine exclusion in Go, `flock`
guarantees process exclusion in the kernel, and neither is inferred from the
other's behaviour on one platform. `TestTheFileLockExcludesGoroutines` is the
regression guard and fails if occupancy ever exceeds one.

The gate has **no TTL**, matching the file locker's own decision. A gate that
expired while its holder still owned the `flock` would let a second goroutine
of the same process pass the gate and then block on a `flock` its own process
holds.

Two smaller consequences of the same measurement: the locker **never calls a
blocking `flock`**, because a blocking `LOCK_EX` parks the thread inside a
syscall no cancellation can reach and `Acquire`'s context would mean nothing;
it polls `LOCK_NB` on the injected clock instead. And it opens **one
description per acquisition** and closes it on release, so the kernel's own
release-on-close is the backstop.

### D8 — Platform: native where the mechanic exists, typed refusal where it does not

Following ADR 0018 and the `internal/service/session` precedent
(`fsguard_unix.go` / `fsguard_other.go`):

- **`flock_unix.go`** — `linux darwin freebsd openbsd netbsd dragonfly`.
- **`flock_other.go`** — everything else. `platformNative` is `false` and
  `NewFileLocker` returns the SDK-wide `proc.UnsupportedPlatform` **at
  construction**.

Windows is refused rather than approximated. It has `LockFileEx`, and it is
tempting to call it the same thing: it is not. `LockFileEx` locks a **byte
range** rather than a file, and its locks are **mandatory** rather than
advisory, so a range lock changes the behaviour of unrelated I/O on the same
file. Every difference is a place where an emulation would behave *almost* like
`flock` — and "almost" is the whole problem for the one primitive whose
failures are invisible when they happen and expensive at every later moment. A
backend that excluded correctly in testing and not under one interleaving is
worth less than no backend, because the caller would have stopped looking.

The in-process locker is stdlib-only and works on **all eight GOOS**, so no
platform is left with nothing.

### D9 — Directory permissions: refuse world-writable-and-not-sticky

`NewFileLocker` refuses a lock directory that is world-writable without the
sticky bit (`LOCK_DIRECTORY_UNSAFE`, `0.3.51.2`).

The exposure is not the counter — it is the **inode**. An account that can
unlink the lock file replaces it with a fresh one, after which a new process
`flock`s the *new* inode while the old holder still `flock`s the old one, and
both are told they hold the same lock. No race is required.

Group-writable is **accepted**: a lock shared between two service accounts
through a common group is a deliberate arrangement, and refusing it would push
callers to a world-writable directory instead. World-writable **with** the
sticky bit is accepted for the same reason — that is exactly what `/tmp` is,
and the sticky bit is precisely the rule that only an entry's owner may unlink
it.

A lock name never reaches the filesystem verbatim: the filename is
`hex(sha256(name)) + ".lock"`. No path separator escapes the directory, no
length limit is hit, and no two distinct names collide through a filesystem's
case folding or Unicode normalisation — each of which silently turns two locks
into one, or one into two.

### D10 — No registry

Like `proc` (ADR 0016), `resilience` (ADR 0026), `net` (ADR 0029), `scheduler`
(ADR 0041), `token` (ADR 0042), `session` (ADR 0045), `validation` (ADR 0046)
and `cache` (ADR 0049).

The reason is specific to locking. The two backends differ in the one property
a caller must know — **whether a lease can be taken from a live holder** — and
resolving one from a configuration string would let a typo swap a lock that
never expires for one that does, silently, with no failure at the moment of the
swap. Every call would still succeed. The difference would surface only as the
rarest class of production bug there is.

### D11 — Distributed locks stay in `third-party/`

A lock over Redis, etcd, ZooKeeper or Consul is a connector to a third-party
system, totally isolated, and belongs under `third-party/` — the same
quarantine ADR 0012 and ADR 0022 applied to vendor-dependent writers and
codecs. Nothing in this domain implies coordination beyond the filesystem it
was given, and the `pkg/v1` doc comment says so.

The honest note for whoever writes one: a distributed lease is where D5's
limitation bites hardest, because network partitions make the stalled-holder
scenario routine rather than rare. Such a backend must issue a fence and must
document that mutual exclusion without one is not guaranteed — the same
sentence, not a weaker one.

## Consequences

- **Core** gains its 19th sibling, `0.2.21.*`: `LOCK_MISCONFIGURED`,
  `LOCK_NOT_HELD`, `LOCK_BACKEND_FAILED`, `LOCK_NAME_REJECTED`.
- **Service** gains `0.3.51.*`: `LOCK_FENCE_CORRUPT`, `LOCK_DIRECTORY_UNSAFE`,
  `LOCK_KEEPALIVE_LOST`. Both ranges are allocated in `codeRangeOwners` in the
  same change (ADR 0035).
- **A context cancellation returns `ctx.Err()`**, not an SDK sentinel: the
  caller supplied the deadline and already knows what it means. This follows
  `resilience`'s existing precedent.
- **No test in the domain sleeps.** Every TTL, takeover, renewal and contention
  assertion is driven by `clock.ManualClock`, enforced by
  `TestPackageNeverWaitsOnTheWallClock`, an AST audit copied from ADR 0041 and
  covering the production sources *and* the suite — the suite is where the
  temptation lives. `TestWallClockAuditDetectsAViolation` proves the audit
  fires on what it claims to catch.
- **`file_contention_unix_test.go` carries the same build constraint as
  `flock_unix.go`.** That is not a rule-12 exclusion: there is no configuration
  in which the code under test is built and the test file is not.
- **No benchmarks ship**, so rule 9 requires no `BENCH.md`. A lock's cost is
  dominated by contention and by one `fsync` per file acquisition, neither of
  which a microbenchmark characterises honestly.

## Why not …

**… put the TTL on `Acquire`?** It reads better at the call site and it is
exactly where a zero gets typed by accident. Putting it in the constructor
makes the dangerous value unsayable in the hot path, which is the shape this
SDK reaches for (`session.Regenerate`, ADR 0045).

**… make the file lock expire too, for symmetry?** Symmetry would require
either a background reaper releasing a working holder's lock behind its back —
the exact fail-open ADR 0031 forbids — or a lease record whose deadline is
enforced against a `flock` the kernel is still granting, which cannot be done
without the holder's cooperation. The asymmetry is the honest answer; `Deadliner`
is how it is communicated.

**… rely on two descriptions excluding each other within a process?** They do,
on Linux, measured. But `flock(2)` is not POSIX, the BSDs are not obliged to
agree, and the same descriptor-sharing subtlety that produces the 8-of-8
measurement is one refactor away from reappearing. The gate is a guarantee; the
measurement is an observation.

**… add a `nameGate` to `internal/kernel`?** It is stdlib-only and generic
enough to qualify under rule 1. It stays unexported here because it has exactly
one consumer and no second one is in sight — recorded rather than dressed up,
the way ADR 0049 recorded `singleflight`'s missing second consumer. If a second
appears, promoting it is a small, reversible change.

**… ship a `WithLock(ctx, name, fn)` convenience?** It would hide which of
`Acquire`'s three outcomes happened and would have to decide, on the caller's
behalf, whether a lost lease aborts `fn`. `Keepalive` makes that decision
explicit instead, and the caller still writes its own `defer`.

## Deferred

- **A Windows backend over `LockFileEx`**, with its own semantics documented
  and its own MUST-fail tests. Refused today rather than approximated (D8).
- **Shared (read) locks.** `flock` has `LOCK_SH` and the in-process locker could
  offer an RWMutex shape, but a shared lease has no meaningful fence — several
  holders are legitimately concurrent — so the ownership story would need
  rewriting rather than extending.
- **A `third-party/lock/redis` connector** (D11). Not started; the note above
  is the specification it must meet.
- **Lock metrics.** Wait time, contention count and takeover count are the three
  numbers an operator wants, and `metrics` (ADR 0044) can carry them. Left out
  to keep the first cut's surface small.

## References

- `internal/core/lock/` — the port, the lease, the sibling, `0.2.21.*`
- `internal/service/lock/` — both lockers, the gate, the fence ledger, the
  keepalive, `0.3.51.*`
- `pkg/v1/lock/` — the public facade
- ADR 0031 — clamp or refuse, and why a zero TTL is the refuse half
- ADR 0018 — build bar / runtime bar and the `UnsupportedPlatform` contract
- ADR 0039 — `Deadliner` as a sibling rather than a widened port
- ADR 0045 — the `flock` + platform-refusal precedent
  (`internal/service/session/fsguard_{unix,other}.go`)
