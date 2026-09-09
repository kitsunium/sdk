# internal/service/lock/

## Purpose

The concrete lockers implementing `internal/core/lock` (**ADR 0052**):

- **`NewMemory`** — excludes the goroutines of one process. Its leases **expire**.
- **`NewFileLocker`** — excludes the processes sharing one directory on one
  machine, through `flock(2)`. Its leases **do not expire**.
- **`Keepalive`** — renews a lease in the background and **cancels a derived
  context** the instant the lease is lost.

Stdlib-only. Code range: `0.3.51.*`, plus the core sentinels `0.2.21.*`.

## The two backends are not two speeds

| | `NewMemory` | `NewFileLocker` |
|---|---|---|
| Scope | goroutines of one process | processes sharing one directory |
| Lease expires | **yes**, after `TTL` | **no** |
| Taken from a live holder | yes | never |
| Implements `core/lock.Deadliner` | **yes** | **no** — and the absence is the API telling you |
| Fence ledger | in-memory map, per name | in the lock file, `fsync`ed, survives reboot |
| Hazard | takeover while the old holder still runs | a hung holder blocks its waiters forever |

The asymmetry comes from a real difference. **A dead process is noticed by the
kernel** — `flock` is released when the last descriptor on the open file
description closes, which process death does — so the failure a TTL exists to
recover from is already handled, and all a TTL would add is the ability to take
the lock from a process that is still alive. **A dead goroutine is noticed by
nobody**, so without expiry one leak deadlocks a name for the life of the
process.

The price of not expiring is stated rather than hidden: a holder that hangs
without dying blocks its waiters indefinitely. That is a **liveness** failure,
visible (every waiter is blocked in `Acquire` on a context whose deadline it
chose), and strictly preferable to a **safety** failure in which two processes
are inside one section, neither blocked, neither logging.

## `flock(2)` excludes nothing between goroutines — measured

Measured on linux/amd64 (kernel 6.12) **before** this backend was written,
because the received wisdom is wrong in a way that only appears under goroutine
concurrency:

| Case | Result |
|---|---|
| Two separate `open(2)` calls, same process, second `LOCK_EX\|LOCK_NB` | `EWOULDBLOCK` — they **do** exclude |
| Two separate descriptions, blocking `LOCK_EX` | blocked — they **do** exclude |
| **Same description re-locked** | **succeeds immediately** — a lock *conversion*, not a wait |
| 8 goroutines sharing one descriptor around a counted section | **max occupancy 8 of 8**, every run |
| `dup(2)`'d descriptor | shares the description; re-lock succeeds; closing the dup does not release |
| Another process while held | `EWOULDBLOCK` — inter-process exclusion works |

Row 4 is decisive: holding **one descriptor for the store's lifetime** is the
natural design, and it makes `flock` a *complete no-op* between goroutines
while working perfectly between processes. The defect is invisible to any test
that spawns processes.

So `fileLocker` takes `nameGate` **before** the `flock` and releases it
**after**: goroutine exclusion is guaranteed by Go, process exclusion by the
kernel, and neither is inferred from the other's behaviour on one platform.
`TestTheFileLockExcludesGoroutines` fails if occupancy ever exceeds one.

Two smaller consequences: the locker **never calls a blocking `flock`** (a
blocking `LOCK_EX` parks the thread inside a syscall no cancellation can reach,
so `Acquire`'s context would mean nothing) — it polls `LOCK_NB` on the injected
clock; and it opens **one description per acquisition**, so the kernel's
release-on-close is the backstop.

`nameGate` has **no TTL**, matching the file lock. A gate that expired while
its holder still owned the `flock` would let a second goroutine of the same
process pass the gate and then block on a `flock` its own process holds.

## Contents

| File | Role |
|---|---|
| `config.go` | `MemoryConfig` + its ADR 0031 **refusal** (a non-positive TTL) |
| `memory.go` | `memoryLocker`: the holding map, the fence ledger, takeover, wake-on-release and wake-on-deadline |
| `memory_lease.go` | `memoryLease`: the only lease that implements `Deadliner` |
| `file_config.go` | `FileConfig` + the ADR 0031 **clamp** (`Poll`) + the directory permission rule |
| `file.go` | `fileLocker`: gate → `flock` → fence, and the reverse on release |
| `file_lease.go` | `fileLease`: no deadline, deliberately |
| `gate.go` | `nameGate` — the in-process half, and the measurement that justifies it |
| `fence.go` | the on-disk ledger: read, increment, `fsync`. Refuses a non-counter |
| `flock_unix.go` / `flock_other.go` | ADR 0018 platform split |
| `keepalive.go` | background renewal → context cancellation with `LOCK_KEEPALIVE_LOST` |

## Platform matrix (ADR 0018)

| GOOS | `NewMemory` | `NewFileLocker` |
|---|---|---|
| linux, darwin, freebsd, openbsd, netbsd, dragonfly | native | native (`flock(2)`) |
| windows, js, plan9, aix, solaris, … | native | **refused at construction** with `proc.UnsupportedPlatform` |

Windows is refused rather than approximated. `LockFileEx` locks a **byte
range** rather than a file and its locks are **mandatory** rather than
advisory, so a range lock changes the behaviour of unrelated I/O on the same
file. Every difference is a place where an emulation would behave *almost* like
`flock`, and "almost" is the whole problem for the one primitive whose failures
are invisible when they happen. Known closure: a `LockFileEx` backend with its
own semantics documented and its own tests (ADR 0052 §Deferred).

## Sentinels

| Sentinel | Code | When |
|---|---|---|
| `LockFenceCorrupt` | `0.3.51.1` | the lock file is not a decimal counter. **Refused, never reset** — a restarted fence reissues numbers the resource already accepted |
| `LockDirectoryUnsafe` | `0.3.51.2` | world-writable, non-sticky lock directory: any account can unlink the lock file and give the next process a **different inode** to `flock` |
| `LockKeepaliveLost` | `0.3.51.3` | a background renewal failed; carried as a context **cause**, never as a return value |

Plus the core sentinels `0.2.21.*`, restated through `errs.Wrap` and never
re-`Define`d here.

## Conventions

- **`Release` ignores its context.** The common call site is
  `defer lease.Release(ctx)` with the very context whose cancellation ended the
  work; honouring it would leak the lease for a full TTL — and, for the file
  locker, hold the `flock` until process exit.
- **A lock name never reaches the filesystem verbatim.** The filename is
  `hex(sha256(name)) + ".lock"`: no traversal, no length limit, no collision
  through case folding or Unicode normalisation.
- **The in-process fence ledger is never pruned.** Forgetting a name's counter
  resets it, and a reset fencing token is not a fencing token. The cost is one
  `uint64` per distinct name for the process's lifetime — **a caller minting
  unbounded distinct names should not use the memory locker.**
- **Nothing here waits on the wall clock**, production or tests, enforced by
  `TestPackageNeverWaitsOnTheWallClock` (the ADR 0041 audit) with
  `TestWallClockAuditDetectsAViolation` proving it fires.

## Do NOT

- **Share one descriptor across acquisitions.** See §`flock(2)` excludes
  nothing. It is the efficient-looking design and it silently removes goroutine
  exclusion.
- **Give `nameGate` a TTL.** It would desynchronise from the `flock` it guards.
- **Replace the `LOCK_NB` poll with a blocking `flock`.** `Acquire`'s context
  would stop meaning anything.
- **"Repair" a corrupt fence ledger.** Refusing is the decision.
- **Make `Keepalive` release the lease on stop.** The lifetime of a lock must
  not depend on the lifetime of a convenience.
- **Add a distributed backend here.** Redis / etcd / Consul are connectors to
  third-party systems and belong under `third-party/` (ADR 0052 §D11).

## Verification

```
bazel test --config=race //internal/service/lock:lock_test
# or: cd internal/service && GOWORK=off go test -race ./lock/...
```

`file_contention_unix_test.go` carries the same build constraint as
`flock_unix.go`, so it runs everywhere the file locker exists and nowhere it
does not. That is **not** a rule-12 exclusion: there is no configuration in
which the code under test is built and the test file is not.
