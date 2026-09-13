# internal/service/lock/

## Purpose

The concrete lockers implementing `internal/core/lock` (**ADR 0052**):

- **`NewMemory`** — excludes the goroutines of one process. Its leases **expire**.
- **`NewFileLocker`** — excludes the processes sharing one directory on one
  machine, through `flock(2)` on Unix and `LockFileEx` on Windows (ADR 0081).
  Its leases **do not expire**.
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
| `flock_unix.go` / `flock_windows.go` / `flock_other.go` | ADR 0018 platform split — `flock(2)`, `LockFileEx`, and the typed refusal |
| `dirsafety_posix.go` / `dirsafety_windows.go` | the lock directory's verdict: a mode-bit rule, and the reason it cannot run on Windows |
| `keepalive.go` | background renewal → context cancellation with `LOCK_KEEPALIVE_LOST` |

## Platform matrix (ADR 0018)

| GOOS | `NewMemory` | `NewFileLocker` |
|---|---|---|
| linux, darwin, freebsd, openbsd, netbsd, dragonfly | native | native (`flock(2)`) |
| windows | native | native (`LockFileEx` — ADR 0081) |
| js, plan9, aix, solaris, ios | native | **refused at construction** with `proc.UnsupportedPlatform` |

## `LockFileEx` is a different primitive, and the differences are measured

Windows was refused until ADR 0081, on the argument that an emulation behaving
*almost* like `flock` is worth less than none. The differences it named are
real; what was missing was a measurement of each one on a real kernel. Taken on
`windows-latest` through the `e2e-cross` lane, before the backend was written:

| Case | `flock(2)` | `LockFileEx` |
|---|---|---|
| Two separate opens, same process | excludes | excludes |
| **Same handle / description re-locked** | **succeeds — a lock conversion** | **refused — `ERROR_LOCK_VIOLATION`** |
| Another process while held | excludes | excludes |
| Enforced against unrelated I/O | no — **advisory** | yes — **mandatory** |
| Scope | the whole file | a **byte range**; this backend takes all 2^64-1 bytes |

Two consequences the code depends on:

- **The `nameGate` is redundant for exclusion on Windows and kept anyway.**
  Row 2 is the reverse of the Unix one, so a second goroutine is excluded by
  the kernel here and by the gate there. The gate stays because it queues
  goroutines on a channel instead of polling a range their own process holds,
  because it makes the two kernels behave identically, and because it is what
  keeps the one-descriptor-for-the-locker's-lifetime refactor from turning a
  silent no-op on Unix into a hard failure here.
- **The fencing ledger is in the locked range, and that is deliberate.** The
  ledger lives at offset 0 of the very file that carries the lock, so a range
  that skipped it would leave the counter writable by every non-holder — the
  mandatory semantics paid for and unused. The holder is exempt per HANDLE, so
  `ReadAt` / `Truncate` / `WriteAt` / `Sync` all work through the descriptor
  that placed the lock; all four are asserted together, because a refusal on
  any one of them would fail every acquisition after taking the lock. The price
  is that a foreign `type` of a HELD lock file fails — which bites least in the
  incident that wants it, since a dead holder's lock is already released.

## The lock directory's verdict is platform-specific, rule AND reason

On Unix: refuse world-writable-and-not-sticky. The whole table — not just the
two extremes the suite used to cover — is pinned by
`TestTheDirectoryRuleIsOtherWriteAndNotSticky`.

On Windows: **accept, and say why.** `os.Stat` has no permission bits to read
and synthesises a mode from `FILE_ATTRIBUTE_READONLY`, so every writable
directory reports `0777` with no sticky bit and the POSIX rule would refuse
**all** of them — ADR 0018 §(a)'s failure mode, wearing a typed error that
blames the deployment. And the attack the rule prevents is already refused by
the open: `os.OpenFile` reaches `CreateFileW` **without** `FILE_SHARE_DELETE`,
so a held lock file can be neither deleted nor renamed whatever the ACL says.
A real DACL check is deferred with its cost in ADR 0081 §Alternatives; the two
residual exposures are named there too.

## Sentinels

| Sentinel | Code | When |
|---|---|---|
| `LockFenceCorrupt` | `0.3.51.1` | the lock file is not a decimal counter. **Refused, never reset** — a restarted fence reissues numbers the resource already accepted |
| `LockDirectoryUnsafe` | `0.3.51.2` | world-writable, non-sticky lock directory: any account can unlink the lock file and give the next process a **different inode** to lock. **Unix only** — Windows has no mode bits to read and the unlink is refused by the open (ADR 0081 §D5) |
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
  nothing. It is the efficient-looking design, it silently removes goroutine
  exclusion on Unix, and it makes every second acquisition fail outright on
  Windows — wrong twice, visible once.
- **Give `nameGate` a TTL.** It would desynchronise from the `flock` it guards.
- **Replace the `LOCK_NB` poll with a blocking `flock`** — or drop
  `LOCKFILE_FAIL_IMMEDIATELY` on Windows, which is the same mistake spelled
  differently. `Acquire`'s context would stop meaning anything.
- **Run the POSIX directory rule on Windows.** It refuses every directory, and
  `prepareDir` only checks directories it did not create — so the symptom is a
  program that starts once on a fresh machine and never again.
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

`file_contention_unix_test.go`, `file_contention_windows_test.go`,
`dirsafety_posix_test.go` and `dirsafety_windows_test.go` each carry the same
build constraint as the file they cover, so each runs everywhere its subject is
compiled and nowhere it is not. That is **not** a rule-12 exclusion of the kind
that hides a test: there is no configuration in which the code under test is
built and its test file is not.

The lane that executes the Windows half is the `windows` job of
`.github/workflows/e2e-cross.yml` (`./lock` is in `SERVICE_PKGS`). The Linux
Bazel gate compiles neither `flock_windows.go` nor its suite, so that lane is
the only gate either has — and the same lane now proves the file locker's
runtime behaviour on macOS and the three BSDs, where it had never run.
