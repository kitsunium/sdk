# internal/service/app/lock/ — reference

Moved unchanged from `internal/service/app/lock/CLAUDE.md` (ADR 0253); each section keeps its title.

## The two backends are not two speeds

| | `NewMemory` | `NewFileLocker` |
|---|---|---|
| Scope | goroutines of one process | processes sharing one directory |
| Lease expires | **yes**, after `TTL` | **no** |
| Taken from a live holder | yes | never |
| Implements `core/app/lock.Deadliner` | **yes** | **no** — and the absence is the API telling you |
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
| `holding.go` | `holding`: one live acquisition of one name — its token, fence and deadline, and the channel its end closes once to wake every waiter |
| `memory_lease.go` | `memoryLease`: the only lease that implements `Deadliner` |
| `file_config.go` | `FileConfig` + the ADR 0031 **clamp** (`Poll`) + the directory permission rule |
| `file.go` | `fileLocker`: gate → `flock` → fence, and the reverse on release |
| `file_lease.go` | `fileLease`: no deadline, deliberately |
| `gate.go` | `nameGate` — the in-process half, and the measurement that justifies it |
| `fence.go` | the on-disk ledger: read, increment, `fsync`. Refuses a non-counter, and the one counter with no successor (ADR 0081 §D7) |
| `backend.go` | `platformNative` = `flock.Native && hardenedOpen` — the kernel lock (`internal/kernel/fs/flock`: `flock(2)`, `LockFileEx`, or nothing) and the hardened open, together or neither — and what each kernel's lock means HERE: why the gate stays on Windows, why the ledger sits inside the locked range |
| `nofollow.go` | the refusal the lock path's PREDICTABILITY makes necessary, and why it closes on both kernels (ADR 0082) |
| `chain.go` | the components ABOVE the lock file, which `O_NOFOLLOW` cannot reach: the `pathchain` walk, and the rule that refuses an indirection only where anybody could have planted it (ADR 0083) |
| `identity.go` | `sameEntry` — is the file this lease holds still the file its NAME leads to? The one exposure here that is DETECTED rather than prevented (ADR 0083) |
| `nofollow_unix.go` / `nofollow_windows.go` / `nofollow_other.go` | the platform split of the open — `O_NOFOLLOW`, `FILE_FLAG_OPEN_REPARSE_POINT` + the handle check, and the plain open — each declaring `hardenedOpen`, on the kernel lock's exact tag sets (`TestTheLockAndItsHardeningShareAPlatform`) |
| `dirsafety_posix.go` / `dirsafety_windows.go` | the lock directory's verdict: a mode-bit rule on Unix, a DACL rule on Windows, and `plantable` — "could anybody create an entry here?", the same question asked of a different directory. The Windows half asks the kernel's DACL reader, `internal/kernel/fs/winacl` (ADR 0084), the masks of its two questions, and decides what "could not look" means here: accept, and log |
| `keepalive.go` | background renewal → context cancellation with `LOCK_KEEPALIVE_LOST` |
| `lock_compliance.go` | the compile-time proof that both lockers and both leases satisfy `core/app/lock`, and that `memoryLease` is a `Deadliner`; the negative for `fileLease` is `TestFileLeaseIsNotADeadliner`. The ports it asserts are generated from `design/app/lock.yaml` (ADR 0163), so a port changed in the design and `kit gen` stop the build here until the engines follow |

## Platform matrix (ADR 0018)

| GOOS | `NewMemory` | `NewFileLocker` |
|---|---|---|
| linux, darwin, freebsd, openbsd, netbsd, dragonfly — and android, ios, which satisfy the `linux` / `darwin` tags | native | native (`flock(2)`) |
| windows | native | native (`LockFileEx` — ADR 0081) |
| js, wasip1, plan9, aix, solaris, illumos | native | **refused at construction** with `proc.UnsupportedPlatform` |

The `NewFileLocker` column is `platformNative` = `flock.Native && hardenedOpen`:
the kernel lock's matrix (`internal/kernel/fs/flock`, whose own suite asserts it
on every GOOS) and this package's hardened open, which share their tag sets by
test rather than by convention.

## `LockFileEx` is a different primitive, and the differences are measured

Windows was refused until ADR 0081, on the argument that an emulation behaving
*almost* like `flock` is worth less than none. The differences it named are
real; what was missing was a measurement of each one on a real kernel. Taken on
`windows-latest` through the `e2e-cross` lane, before the backend was written —
the backend that is now the kernel's `internal/kernel/fs/flock`, whose
`flock_windows.go` carries the same table; the two consequences below are this
domain's, and live in `backend.go`:

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

Two questions, not one — *can any account REPLACE an entry here?* for
`checkDir`, *can any account CREATE one?* for `plantable` — and two
vocabularies for them. The Unix pair differ over the sticky bit; the Windows
pair differ over which access bits they read (ADR 0086).

On Unix: refuse world-writable-and-not-sticky. The whole table — not just the
two extremes the suite used to cover — is pinned by
`TestTheDirectoryRuleIsOtherWriteAndNotSticky`.

On Windows: read the **DACL** (ADR 0084). `os.Stat` has no permission bits and
synthesises a mode from `FILE_ATTRIBUTE_READONLY`, so every writable directory
reports `0777` with no sticky bit and the POSIX rule would refuse **all** of
them — ADR 0018 §(a)'s failure mode wearing a typed error that blames the
deployment. So the rule asks instead whether an access-allowed entry grants a
right to Everyone (`S-1-1-0`), Authenticated Users (`S-1-5-11`) or
BUILTIN\Users (`S-1-5-32-545`) — and WHICH right depends on which of the two
rules is asking (ADR 0086 §D1):

| mask (`internal/kernel/fs/winacl`) | rights | asked by |
|---|---|---|
| `ReplaceRights` | `FILE_DELETE_CHILD`, `WRITE_DAC`, `WRITE_OWNER` | `checkDir`, of the lock directory |
| `CreateRights` | `FILE_ADD_SUBDIRECTORY`, `WRITE_DAC`, `WRITE_OWNER` | `plantable`, of an indirection's container |
| `ContentRights` | `FILE_WRITE_DATA`, `FILE_APPEND_DATA`, `DELETE`, `WRITE_DAC`, `WRITE_OWNER` | `checkDir`, of the entries carrying `OBJECT_INHERIT_ACE` |

The third mask has no Unix counterpart: there a lock file is created `0600`
whatever the directory's mode says, here it INHERITS the directory's list — so
a directory nobody can unlink from can still hand every account the fencing
ledger.

It was deferred three times on a cost estimate of ~250 lines of ABI that
neither carrying record re-checked. It is two `advapi32` exports:
`GetNamedSecurityInfoW` and `GetAce`. `syscall` already ships `StringToSid`,
`(*SID).String` and `LocalFree`, so `EqualSid` is not bound at all — the
comparison is byte equality on the rendered SID. The reader this package wrote
is the kernel's now, `internal/kernel/fs/winacl` (ADR 0159), because the queue
asked it the same question through this package's exported surface — a
service-to-service edge for one Windows function; the masks it is asked and
what an unanswered question means stay here.

There **is** a sticky equivalent here, spelled in two bits instead of one:
`FILE_ADD_FILE` without `FILE_DELETE_CHILD`. `%ProgramData%` grants
BUILTIN\Users exactly that, on itself and on every subdirectory — measured on
`windows-latest` — which is why a lock directory under it is ACCEPTED and a
junction planted beside it is REFUSED. ADR 0084 said the shape did not exist;
ADR 0086 §D2 corrects it, and that correction is what let the third identifier
into the table without refusing every machine-wide Windows deployment.

All **eight** discretionary ACE shapes are decoded (ADR 0086 §D6): the plain
pair, the object pair that puts 0-2 GUIDs between the mask and the SID, and
the two callback pairs that trail a conditional expression. An NTFS directory
does store an object-type entry — measured, against ADR 0084's premise — and
the expression a callback entry carries is never evaluated: a conditional
allow is read as granting, a conditional deny as denying nothing.

The check **fails open**: any failure on the way to a verdict accepts. Failing
open *silently* would be a different thing and is not what happens —
`winacl.GrantsAnyone` returns an empty `observed` when a verdict was reached
and names what stopped it when it was not — the Win32 status
(`GetNamedSecurityInfoW=5`), a path that is not one (`UTF16PtrFromString=…`),
the entry the walk could not fetch (`GetAce#3`) — and both `checkDir` and
`checkChain` **log** the second case before accepting. The last two used to
come back empty, indistinguishable from a verdict; ADR 0095 named them so the
queue, which asks the same reader, can refuse where the lock accepts.
`TestEveryAnswerWithoutAVerdictIsNamed`, which moved with the reader to
`internal/kernel/fs/winacl`, drives the two a test can cause (a NUL in the
path, a directory that is not there) against a list read to the end. The log is the same channel
`framework/internal/service/entitlement` uses for the same shape of degradation, and it
fires only when the platform API refused to answer.

## The lock directory's rule, beside the other four

`proc/ipc`, `security/secret`, `security/session` and `data/queue` each refuse
a directory by a rule of their own, compared side by side in
`internal/kernel/fs/CLAUDE.md` §Five directory rules. They share the kernel's
measurements (`pathchain`, `winacl`) and lock (`flock`), and no rule — a
consolidation into one parameterised check was proposed and refused, because
each difference below is a decision this domain made for a reason the others
do not have:

- **Group-writable is accepted, and so is `0777|sticky`.** A lock exists to be
  SHARED — between two service accounts through a group, or every account
  through `/tmp` — so the rule refuses only what lets a stranger REPLACE a lock
  file and split one lock into two inodes. `ipc`, `secret` and `session`
  refuse a group bit because their directory is one account's; their rule
  here would refuse the deployments this domain exists for. `queue` makes the
  same decision for the same reason, at its root.
- **Ownership is never checked**, of the directory or of a component above it,
  where `ipc` checks both: the entry may belong to the OTHER account by design
  (ADR 0082 §Deferred, and §Unlink-and-replace below).
- **An unreadable Windows list is ACCEPTED and logged**; `queue` refuses the
  same answer from the same reader (ADR 0084 §D5, ADR 0095).
- **The path above is audited on both kernels, before `MkdirAll`.** `session`
  adopts the Unix half verbatim; `secret` inherits it by taking this package's
  file locker over its own directory — after its own `MkdirAll`, so a refused
  path may already hold the new, empty directory.
- **Nothing is narrowed**: an existing directory is checked, never changed —
  `session` alone narrows, and only a directory it created.

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
`internal/kernel/fs/pathchain` **before** `os.MkdirAll` — auditing afterwards
means refusing the directory only after creating it inside the planter's tree.

The rule is **not** "refuse a link". `/tmp` is a symbolic link on macOS,
`/var/run` is one on most Linux distributions, `C:\Users\All Users` is a
junction. An indirection is refused when the directory **holding** it is
world-writable — when anybody could have planted it — and the sticky bit
exempts nothing, because planting a component CREATES an entry rather than
unlinking one. That is ADR 0082's own argument, one level up.

On **Windows the same rule runs**, over a different answer to the same
question. `os.Stat` synthesises `0777` for every writable directory there, so
the mode says nothing — the verdict comes from the directory's DACL instead
(`internal/kernel/fs/winacl`, ADR 0084). A junction in a directory only its owner can
write is ACCEPTED, because `C:\Users\All Users -> C:\ProgramData` is one
Windows installs itself; a junction in a directory Everyone can write is
refused. Both rows are tested on `windows-latest` with real ACLs applied
through `icacls`.

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

The lockers' five, declared with their codes in `internal/core/app/lock`
(ADR 0160) and raised here:

| Sentinel | Code | When |
|---|---|---|
| `LockFenceCorrupt` | `0.3.51.1` | no next token can be issued: the lock file is not a decimal counter (`condition=unparseable`), or it holds `2^64-1` and `previous+1` would wrap to zero (`condition=exhausted`). **Refused, never reset** — a restarted fence, and a wrapped one, both reissue numbers the resource already accepted |
| `LockDirectoryUnsafe` | `0.3.51.2` | a lock directory whose entries any account can REPLACE: world-writable and non-sticky on Unix, or a DACL letting Everyone / Authenticated Users / BUILTIN\Users unlink somebody else's entry or write the files created there, on Windows (ADR 0084, ADR 0086). Either way the lock file can be replaced and the next process locks a **different inode** |
| `LockKeepaliveLost` | `0.3.51.3` | a background renewal failed; carried as a context **cause**, never as a return value |
| `LockPathRedirected` | `0.3.51.4` | the lock path is a symbolic link (Unix) or a reparse point (Windows), so the lock and its ledger would land on a file whoever planted it chose. **Not** `LOCK_BACKEND_FAILED`: nothing failed, and that code invites the one wrong response — a retry. Since ADR 0083 it also covers an indirection at a PARENT component planted where anybody could have planted it, with the component, the configured directory and the target in its fields |
| `LockFileReplaced` | `0.3.51.5` | the file a lease holds is no longer the file its name leads to — unlinked, or replaced, while held. Reported at `Acquire` (the window between the open and the `flock`) and at `Extend` (the only call a holder makes during the section). **Detection, never prevention**: the split still happens and the holder is told |

Plus the port's sentinels `0.2.21.*`, restated through `errs.Wrap`. No
sentinel is `Define`d here.

## Verification

`file_contention_unix_test.go`, `file_contention_windows_test.go`,
`dirsafety_posix_test.go`, `dirsafety_windows_test.go`,
`nofollow_unix_test.go` and `nofollow_windows_test.go` each carry the same
build constraint as the file they cover — for the two contention suites, the
kernel lock's `flock_*.go` in `internal/kernel/fs/flock` — so each runs
everywhere its subject is compiled and nowhere it is not. That is **not** a
rule-12 exclusion of the kind that hides a test: there is no configuration in
which the code under test is built and its test file is not.
`backend_internal_test.go` carries no constraint and asserts on every GOOS that
the kernel lock and the hardened open share a platform.

The lane that executes the Windows half is the `windows` job of
`.github/workflows/e2e-cross.yml` (`./app/lock` is in `SERVICE_PKGS`;
`./fs/flock` and `./fs/winacl`, the two kernel primitives under it, are in
`KERNEL_PKGS`). The Linux Bazel gate compiles neither the kernel's
`flock_windows.go` and DACL reader nor this package's Windows suite, so that
lane is the only gate any of them has — and the same lane now proves the file
locker's runtime behaviour on macOS and the three BSDs, where it had never run
— and, since ADR 0144, on illumos and Solaris, where what it proves is the
refusal at construction.
