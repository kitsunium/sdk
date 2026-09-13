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
| `fence.go` | the on-disk ledger: read, increment, `fsync`. Refuses a non-counter, and the one counter with no successor (ADR 0081 §D7) |
| `flock_unix.go` / `flock_windows.go` / `flock_other.go` | ADR 0018 platform split — `flock(2)`, `LockFileEx`, and the typed refusal |
| `nofollow.go` | the refusal the lock path's PREDICTABILITY makes necessary, and why it closes on both kernels (ADR 0082) |
| `chain.go` | the components ABOVE the lock file, which `O_NOFOLLOW` cannot reach: the `pathchain` walk, and the rule that refuses an indirection only where anybody could have planted it (ADR 0083) |
| `identity.go` | `sameEntry` — is the file this lease holds still the file its NAME leads to? The one exposure here that is DETECTED rather than prevented (ADR 0083) |
| `nofollow_unix.go` / `nofollow_windows.go` / `nofollow_other.go` | the same split again — `O_NOFOLLOW`, `FILE_FLAG_OPEN_REPARSE_POINT` + the handle check, and the plain open |
| `dirsafety_posix.go` / `dirsafety_windows.go` | the lock directory's verdict: a mode-bit rule, the reason it cannot run on Windows, and `plantable` — "could anybody create an entry here?", which is the same question asked of a different directory |
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

## The directory rule never reached the lock file's NAME

`checkDir` refuses a directory whose entries any account can UNLINK. The attack
it was written against has a twin it does not touch: **creating** an entry at a
name nobody has taken yet. The lock filename is `hex(sha256(name)) + ".lock"` —
derived from no caller string, and entirely PREDICTABLE — which is all a
planter needs. The sticky
bit does not help, because the planter owns the link they created, and
`0777|sticky` — what `/tmp` is — is a row the table explicitly ACCEPTS.

Probed against the shipped locker on linux/amd64, in exactly that directory:

```
répertoire 0777|sticky : ACCEPTÉ
acquisition sur lien planté : held=true err=<nil>
SUIVI : le verrou a atterri sur /tmp/sym.../elsewhere.lock
```

`held=true`, no error, and the `flock` and the fencing ledger outside the
checked directory. Two processes each hold "the same" lock over different
inodes, neither blocked, nothing logged. The fence goes with it: a link to a
file containing `48213\n` — a pidfile is that shape — made `Acquire` return
fence **48214** and rewrite the pidfile with it.

`openLockFile` closes it on both kernels (ADR 0082), and the two mechanisms are
opposites in the same way `flock`/`LockFileEx` are:

| | Unix | Windows |
|---|---|---|
| Asks the kernel for | `O_NOFOLLOW` — do not traverse | `FILE_FLAG_OPEN_REPARSE_POINT` — open the LINK |
| The open | **fails** | **succeeds**, on the link |
| What refuses | the kernel | `refuseReparseHandle`, on the handle's attributes |
| Sentinel | `LockPathRedirected` | `LockPathRedirected` |

## The two things ADR 0082 left open, and which of them is PREVENTED

ADR 0083 closes both. They are not closed the same way, and the difference is
the most important sentence in this file.

### A link at a PARENT component — prevented

`O_NOFOLLOW` governs the FINAL component. A link planted at any parent of
`FileConfig.Dir` moved the whole lock directory, and `checkDir` then took its
verdict on the TARGET — so a planter redirecting into a tidy `0700` directory
of their own passed the mode rule too:

```
Dir demandé  = …/pub/myapp/locks
composant planté = …/pub/myapp -> …/elsewhere
construction ACCEPTÉE
acquisition sur parent planté : err=<nil>
SUIVI : le verrou a atterri sur …/elsewhere/locks/a4d268….lock
```

`checkChain` (chain.go) now walks every component through
`internal/kernel/pathchain` **before** `os.MkdirAll` — auditing afterwards
means refusing the directory only after creating it inside the planter's tree.

The rule is **not** "refuse a link". `/tmp` is a symbolic link on macOS,
`/var/run` is one on most Linux distributions, `C:\Users\All Users` is a
junction. An indirection is refused when the directory **holding** it is
world-writable — when anybody could have planted it — and the sticky bit
exempts nothing, because planting a component CREATES an entry rather than
unlinking one. That is ADR 0082's own argument, one level up.

On **Windows nothing new is refused**, and `dirsafety_windows.go` says why:
`os.Stat` synthesises `0777` for every writable directory, so the rule would
refuse every junction under one. `TestAnIndirectionAboveTheLockFileIsAccepted
OnWindows` pins that gap on a real kernel rather than leaving it assumed.

### Unlink-and-replace in a sticky directory — DETECTED, not prevented

```
victime détient le verrou : fence=1 inode=69831
2e verrou AVANT l'échange : held=false (attendu false)
entrée désliée pendant que la victime la détient
2e verrou APRÈS l'échange : held=true err=<nil> inode=69832
SPLIT : deux détenteurs, fences 1 et 1, inodes 69831 et 69832
Extend de la victime : <nil>
```

No flag prevents this. The entry is unlinked AFTER the open, by an account the
directory's permissions genuinely allow to unlink it, and the descriptor
outlives the name on every kernel. Two remedies were evaluated and prevent
nothing: an owner check breaks the shared-group arrangement `checkDir`
deliberately accepts (ADR 0081 §D5), and `O_EXCL` answers who CREATED the file,
which is not the question.

So the last line changes and nothing else does. `Extend` compares the
descriptor against the name (`identity.go`) and returns `LOCK_FILE_REPLACED`;
a `Keepalive` turns that into a cancelled context for the work inside the
section. `TestTheSplitIsDetectedAndNotPrevented` asserts that the second holder
**still acquires**, deliberately, so no reader can come away believing the
exclusion was restored. The only prevention is a lock directory no other
account can write — which is what `NewFileLocker` creates (`0700`) when the
directory is absent.

The errno is never consulted: `O_NOFOLLOW` on a symlink is measured `ELOOP` on
linux/amd64 and is documented `EMLINK` on FreeBSD/DragonFly, `EFTYPE` on
NetBSD, `ELOOP` on OpenBSD/Darwin. `os.Lstat` after the refusal answers what
the errno was only evidence for, identically on all six, and it is **diagnosis
rather than the decision** — the kernel already refused one line earlier, so
there is nothing to race.

## Sentinels

| Sentinel | Code | When |
|---|---|---|
| `LockFenceCorrupt` | `0.3.51.1` | no next token can be issued: the lock file is not a decimal counter (`condition=unparseable`), or it holds `2^64-1` and `previous+1` would wrap to zero (`condition=exhausted`). **Refused, never reset** — a restarted fence, and a wrapped one, both reissue numbers the resource already accepted |
| `LockDirectoryUnsafe` | `0.3.51.2` | world-writable, non-sticky lock directory: any account can unlink the lock file and give the next process a **different inode** to lock. **Unix only** — Windows has no mode bits to read and the unlink is refused by the open (ADR 0081 §D5) |
| `LockKeepaliveLost` | `0.3.51.3` | a background renewal failed; carried as a context **cause**, never as a return value |
| `LockPathRedirected` | `0.3.51.4` | the lock path is a symbolic link (Unix) or a reparse point (Windows), so the lock and its ledger would land on a file whoever planted it chose. **Not** `LOCK_BACKEND_FAILED`: nothing failed, and that code invites the one wrong response — a retry. Since ADR 0083 it also covers an indirection at a PARENT component planted where anybody could have planted it, with the component, the configured directory and the target in its fields |
| `LockFileReplaced` | `0.3.51.5` | the file a lease holds is no longer the file its name leads to — unlinked, or replaced, while held. Reported at `Acquire` (the window between the open and the `flock`) and at `Extend` (the only call a holder makes during the section). **Detection, never prevention**: the split still happens and the holder is told |

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
- **Open the lock file with a bare `os.OpenFile`.** That is the defect ADR 0082
  closed, and it is invisible: the acquisition succeeds. Go through
  `openLockFile`.
- **Read `LOCK_FILE_REPLACED` as a lock that can be re-taken.** Re-acquiring
  hands the caller a second lease over the new file while the old one is still
  locked, which is the split-brain spelled deliberately. Stop the work.
- **Make `sameEntry` refuse on an inconclusive answer.** A stat this process is
  not allowed to take, or a medium that did not respond, must read as
  unchanged: a lock that stops renewing on a stat hiccup fails its caller
  harder than the attack it watches for, and there is no third verdict.
- **Move `checkChain` after `prepareDir`.** `os.MkdirAll` follows a planted
  parent, so the audit would refuse the directory only after having created it
  inside the planter's tree.
- **Refuse a link at a parent unconditionally.** It refuses `/tmp` on macOS and
  `/var/run` on most Linux distributions — ADR 0018 §(a)'s failure mode with an
  error that blames the operator for the operating system's own layout.
- **Give the Unix `nofollow` file to Solaris** because it has `O_NOFOLLOW`.
  The tag sets of `flock_*.go` and `nofollow_*.go` are identical on purpose —
  a platform gains a lock and its hardening together or gains neither.
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
`dirsafety_posix_test.go`, `dirsafety_windows_test.go`,
`nofollow_unix_test.go` and `nofollow_windows_test.go` each carry the same
build constraint as the file they cover, so each runs everywhere its subject is
compiled and nowhere it is not. That is **not** a rule-12 exclusion of the kind
that hides a test: there is no configuration in which the code under test is
built and its test file is not.

The lane that executes the Windows half is the `windows` job of
`.github/workflows/e2e-cross.yml` (`./lock` is in `SERVICE_PKGS`). The Linux
Bazel gate compiles neither `flock_windows.go` nor its suite, so that lane is
the only gate either has — and the same lane now proves the file locker's
runtime behaviour on macOS and the three BSDs, where it had never run.
