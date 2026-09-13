# ADR 0081 — the Windows file lock: a different primitive, measured rather than recited, and the one rule it cannot run

- **Status**: Accepted
- **Date**: 2026-09-13
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0052](0052-sdk-lock-domain.md) §D8 (Windows was refused at construction; it is now implemented, and §D7's measurement gains its opposite number)
- **Related**: [ADR 0018](0018-sdk-cross-platform-portability.md) (build bar + runtime bar, the uniform `UnsupportedPlatform`, the `x/sys` ban and the hand-cited-ABI discipline it imposes), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (clamp vs refuse), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (`Deadliner` as a sibling), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (a file goes in the layer that owns the concern)

## Context

ADR 0052 §D8 refused Windows at construction and said why:

> `LockFileEx` locks a **byte range** rather than a file, and its locks are
> **mandatory** rather than advisory, so a range lock changes the behaviour of
> unrelated I/O on the same file. Every difference is a place where an
> emulation would behave *almost* like `flock` — and "almost" is the whole
> problem.

Every one of those sentences is true. The conclusion drawn from them is not.
"Almost" is a problem when the differences are unknown; it is a specification
when they are measured, written down, and pinned by tests that fail when they
change. What ADR 0052 actually lacked was a real Windows kernel to measure on,
and `.github/workflows/e2e-cross.yml` has had a `windows-latest` runner the
whole time.

The cost of leaving it refused stopped being hypothetical. `kodflow/ktn-linter`
moved its MCP daemon's single-instance lock onto `pkg/v1/lock` and **the daemon
stopped starting on Windows entirely** — `NewFileLocker` returns
`UnsupportedPlatform`, the caller propagates it, `Run()` aborts. The consumer
restored a local `LockFileEx` implementation as a stopgap whose package doc
says it is deleted the day this lands. A refusal a consumer has to work around
by reimplementing the thing refused is not a conservative default; it is the
SDK exporting the decision it declined to take.

There was also a second blocker, less visible than the build tag and more
dangerous. `checkDir` in `file_config.go` carried **no build tag at all** and
read POSIX mode bits. Flipping `platformNative` alone would have shipped a
locker that refuses every directory on the platform it was just enabled for —
and refuses it as `LOCK_DIRECTORY_UNSAFE`, which reads to an operator as a
deployment fault rather than a bug in this package. Worse, `prepareDir` checks
only directories it did **not** create, so the symptom would have been a daemon
that starts once on a fresh machine and never again.

## Decision

### D1 — A `LockFileEx` backend, bound from kernel32, with no new dependency

`flock_windows.go` implements `flockTry` / `flockUnlock` and sets
`platformNative` to `true`. `golang.org/x/sys/windows` is where `LockFileEx`
normally comes from and it is **banned SDK-wide** (ADR 0018 §Dependency
constraint). The stdlib `syscall` package does not export `LockFileEx` or
`UnlockFileEx` on Windows either — verified, `go doc syscall.LockFileEx` for
`GOOS=windows` reports *"no symbol LockFileEx in package syscall"*.

So the two entry points are bound from `kernel32.dll` with
`syscall.NewLazyDLL`, with the flags and the one error code hand-declared and
cited to `winbase.h` / `winerror.h`. That is not a workaround invented here: it
is the discipline ADR 0018 prescribes and the shape
`internal/service/proc/exec` (Job Objects), `internal/service/proc/cgroup` and
`internal/service/proc/signal` already use. `kernel32.dll` is in the Go
runtime's own system-DLL set, so `syscall.LoadDLL` resolves it from SYSTEM32
and the preloading concern its doc comment raises does not apply.

**The ban is therefore not a blocker, and the SDK gains no module, no `go.sum`
entry and no `MODULE.bazel` change.** Nothing about the four-layer contract
moves either: this is a platform backend for a port that already exists, so it
belongs in `internal/service/lock` beside `flock_unix.go` (ADR 0074), and
`third-party/` — the documented quarantine for a banned dependency (ADR
0012/0022/0034) — would be exactly the wrong place, since a quarantine is for
a dependency and there is no dependency.

### D2 — The range is the WHOLE file, because the fencing ledger is in it

`LockFileEx` locks a byte range, so a range has to be chosen. It is offset
zero, `MAXDWORD` in both halves of the length: the documented "everything"
idiom, legal past end-of-file.

The alternative is one byte at an offset nothing will occupy. It excludes
just as well — measured — and it keeps the lock file readable by anyone while
held, which is a real property: `fence.go` writes the counter in decimal
precisely so it can be read during an incident. It is rejected anyway, because
the **fencing ledger lives at offset 0 of this very file**. A range that does
not cover it leaves the counter writable by every non-holder, which is what
`flock(2)` already leaves it as — so the mandatory semantics would be paid for
in full and used for nothing.

`TestAHighOffsetRangeLeavesTheLedgerUnprotected` measures the rejected option
rather than describing it: it takes the high-offset lock, confirms a second
handle is still excluded, and then rewrites the ledger through that excluded
handle.

### D3 — The locks are MANDATORY, and both consequences are kept

The kernel enforces them against ordinary reads and writes.

- **The ledger is strictly better protected here than under `flock(2)`.** A
  foreign handle's read *and* write of the locked region both fail —
  `TestTheRangeLockIsMandatoryAgainstAForeignHandle`. `LOCK_FENCE_CORRUPT`
  detects a wrecked counter after the fact; on Windows a non-holder cannot
  wreck it while a holder exists.
- **A foreign `type` of a HELD lock file fails** with `ERROR_LOCK_VIOLATION`.
  That is a genuine loss against `flock(2)`, and it is stated rather than
  buried. It bites least where it matters most: the incident in which someone
  wants to read the fence is usually one where the holder died, and a dead
  holder's lock is already released by the kernel.

The holder itself is exempt — the rule is per HANDLE, and the handle that
placed the lock keeps full access. **That is what the whole design rests on**,
because `readFence`/`writeFence` operate on the very descriptor that carries
the lock, through exactly four operations: `ReadAt`, `Truncate`, `WriteAt`,
`Sync`. `Truncate` is the one with no exemption of its own in the
documentation — it is `SetEndOfFile` over a range this handle holds — so all
four are asserted together on a real kernel by
`TestTheLedgerSurfaceWorksThroughTheLockingHandle`. If any were refused, the
backend would take the lock and then fail to mint a fence on **every**
acquisition.

### D4 — A second lock inside one process: the two kernels are opposites

ADR 0052 §D7 measured `flock(2)` and found the received wisdom wrong:
re-locking a description that already holds `LOCK_EX` is a lock CONVERSION that
succeeds immediately, so eight goroutines sharing one descriptor were all
inside one section at once — 8 of 8, every run.

Measured on `windows-latest`, `LockFileEx` comes out the other way:

| Case | `flock(2)` (ADR 0052 §D7) | `LockFileEx` (here) |
|---|---|---|
| Two separate opens, same process | excludes | **excludes** |
| **The same handle / description re-locked** | **succeeds — a conversion** | **refused — `ERROR_LOCK_VIOLATION`** |
| Another process while held | excludes | **excludes** |
| Enforced against unrelated I/O | no — advisory | **yes — mandatory** |

Rows 2 and 4 are the divergences. The second is the one that matters for this
package: **the in-process `nameGate` is not load-bearing for exclusion on
Windows.** It stays anyway, and the reason is different from the one that put
it there:

1. It keeps one process's goroutines **queued on a channel** instead of polling
   a range their own process already holds. Without it `TryAcquire` between two
   goroutines is still correct, but `Acquire` burns a poll interval per round.
2. It keeps the package's **behaviour identical on both kernels**, so a suite
   that passes on Linux means something here. An exclusion that is structural
   on one platform and emergent on another is an exclusion nobody can reason
   about.
3. It is what keeps the natural "one handle for the locker's lifetime" refactor
   — the efficient-looking design ADR 0052 §D7 warns about — from turning a
   **silent no-op on Unix** into a **hard failure on Windows**. Both are wrong;
   only one is visible; the gate makes neither reachable.

So: the gate is **redundant for exclusion and necessary for everything else**,
and this ADR says so rather than leaving a reader to infer that the Unix
justification carries over unchanged. It does not.

### D5 — `checkDir` splits by build tag; Windows accepts, and says why

`checkDir` and `worldWritable` move out of `file_config.go` into
`dirsafety_posix.go` (`!windows`) and `dirsafety_windows.go` (`windows`).
`prepareDir` is unchanged and still calls `checkDir`.

**On Unix nothing changes.** The rule is still "refuse when the other-write bit
is set AND the sticky bit is not", and the split is guarded by a table that
did not exist before, covering all seven cases rather than the two the suite
had — because a split can preserve *"refuses the obvious one"* while dropping
*"accepts the deliberate ones"* and nothing would say so:

| mode | verdict |
|---|---|
| `0700` owner-only | accepted |
| `0770` group-writable | accepted |
| `0750` group-readable | accepted |
| `0777` world-writable, no sticky | **refused** |
| `0777` world-writable **with** sticky | accepted |
| `0707` world-writable, no group | **refused** |
| `0702` world-WRITE only | **refused** |

**On Windows the verdict is: accept, with the gap named.** Two measurements,
not a shrug:

1. **The rule has no input here.** Windows has no permission bits; `os.Stat`
   synthesises a mode from the single `FILE_ATTRIBUTE_READONLY` flag — `0444`
   set, `0666` unset, plus `ModeDir|0111` for a directory — so **every**
   writable directory reports `0777` with no sticky bit and the predicate is
   true for all of them. Running the rule would refuse every directory a caller
   could name: ADR 0018 §(a)'s exact failure mode, wearing a typed error that
   blames the deployment.
2. **The attack it prevents is refused by the open, not by the directory.**
   What the POSIX rule protects is the INODE — unlink the lock file, the next
   process locks the new one, both are told they hold the same lock. `os.OpenFile`
   reaches `CreateFileW` with `FILE_SHARE_READ|FILE_SHARE_WRITE` and
   deliberately **without** `FILE_SHARE_DELETE` (Go 1.27,
   `src/syscall/syscall_windows.go`, `func Open`), so while any holder has the
   lock file open **neither a delete nor a rename can touch it**, whatever the
   ACL permits.

Both are asserted on the real kernel, not read off the stdlib source:
`TestThePosixDirectoryRuleWouldRefuseEveryDirectory` and
`TestTheLockFileCannotBeUnlinkedOrRenamedWhileItIsOpen`. The second is the one
that matters most — it fails loudly if a future Go release adds the share flag,
which would silently reopen the exposure this decision is built on.

### D6 — The lane that runs it is named in the same change

`internal/service/lock` joins `SERVICE_PKGS` in
`.github/workflows/e2e-cross.yml`. Without it the Windows suite would compile
and never run, which is worth less than no suite — CLAUDE.md rule 12's exact
failure mode. The Linux Bazel gate compiles neither the backend nor its suite,
so this lane is the only gate either has, and `./lock` now also runs on macOS
and the three BSDs, where the file locker's runtime behaviour had never been
proven.

The backend was written **after** its tests had been seen failing on that lane.

## Consequences / Semantics

- **The platform matrix gains a second native column.** `NewFileLocker` works
  on `windows` as it does on `linux`, `darwin` and the four BSDs.
  `UnsupportedPlatform` is now returned only where there is no file-range lock
  at all: `js`, `plan9`, `aix`, `solaris`, `ios`.
- **The contract is unchanged.** Same `Locker`, same `Lease`, same sentinels,
  no new error code, no new configuration field. A file lease still does not
  implement `Deadliner`, for the same reason: the kernel releases the lock when
  the holder's process dies, so there is no deadline to report.
- **A fence is still a fence across a reboot**, and on Windows it is harder to
  corrupt: a non-holder cannot write the ledger while a holder exists.
- **A held lock file is not readable by a non-holder on Windows.** The one
  property the mandatory semantics cost, named here and in the package doc.
- **The dependency graph is untouched**: no module added to any `go.mod`, no
  `go.sum` entry, no `MODULE.bazel` change. `pkg` consumers inherit nothing.
- **Negative / accepted.** The lock directory's permissions are not checked on
  Windows, so a world-writable lock directory is accepted there. The inode
  swap is unreachable while a holder exists (D5.2); what remains is a denial of
  service — an attacker who can write the directory can hold the lock first —
  and that is a liveness failure of the kind this domain already prefers to a
  safety one (ADR 0052 §D6).
- **Negative / accepted.** The kernel32 ABI constants are hand-maintained, with
  citations in the source, which is the standing cost of the `x/sys` ban
  (ADR 0018 §Consequences). The `e2e-cross` `windows` lane is the safety net,
  and it now runs on every push.

## Breaking changes

**One, and it is deliberate.** `NewFileLocker` no longer returns
`proc.UnsupportedPlatform` on Windows. A caller that branches on that sentinel
to fall back to `NewMemory` will now receive a real, machine-scoped file locker
— which is the point, but it is a behaviour change and not merely an addition.

The direction is the safe one: the fallback narrowed from
*"goroutines of one process"* to *"processes on one machine"*, so nothing that
was excluded before stops being excluded. A caller who genuinely wants the
in-process locker on Windows has always been able to ask for it by name.

No API changed. No type, function, field or sentinel was added, removed or
renamed.

## Alternatives considered

**Allow `golang.org/x/sys/windows` for this one backend.** It supplies
`LockFileEx` ready-made. Rejected for the reason ADR 0018 already gives, and
the measurement confirms there is nothing to trade: the hand-bound version is
two `NewProc` calls and four cited constants, and it costs the SDK no module.
A ban relaxed once for convenience is a ban with a precedent attached.

**Quarantine the backend under `third-party/`.** The documented answer for a
banned dependency (ADR 0012, ADR 0022/0034, ADR 0078). It does not apply: a
quarantine isolates a *dependency*, and there is no dependency here — only
stdlib `syscall`. Quarantining would also put a platform backend outside the
package that owns the port, so `NewFileLocker` could not select it, and Windows
consumers would import a different constructor from a different module to get
the same contract. That is the four-layer violation the quarantine was invented
to avoid, not an instance of it.

**Lock one byte at a high offset instead of the whole file.** Keeps the ledger
readable while held; loses the ledger's protection. Measured, not argued —
`TestAHighOffsetRangeLeavesTheLedgerUnprotected` rewrites the counter through a
handle the lock has just excluded. Rejected: the readability is worth less than
the protection, and it is worth least in the incident that wants it.

**Drop the `nameGate` on Windows, since `LockFileEx` already excludes
goroutines.** Rejected on all three counts in D4. The strongest is the third:
the gate is the only thing that makes the descriptor-per-acquisition detail an
optimisation rather than a load-bearing invariant, and it is load-bearing in
opposite directions on the two kernels.

**Implement a real DACL check for `checkDir` on Windows.** `GetNamedSecurityInfoW`
from `advapi32.dll`, walk the DACL, refuse an ACE granting `FILE_WRITE_DATA` or
`DELETE` to `S-1-1-0` or `S-1-5-11`. This is the answer that matches the Unix
rule's *intent* rather than its mechanism, and it was rejected on cost against
value, not on principle:

- It is roughly 250 lines of hand-declared ABI — `SECURITY_DESCRIPTOR`, `ACL`,
  `ACE_HEADER`, `ACCESS_ALLOWED_ACE`, SID comparison, inherited-ACE handling —
  every byte of which is the `x/sys` ban's maintenance cost, in the one place
  where getting it wrong has two failure modes and both are bad: refuse
  everything (ADR 0018 §(a) again) or accept everything (what we do now, minus
  the honesty).
- It cannot be iterated on locally. Every revision is a CI round trip to a
  Windows runner, which is exactly the condition under which an
  almost-right ACL walk ships.
- And it would be guarding an exposure D5.2 shows is already closed by the
  kernel for as long as a holder exists.

A documented acceptance with both measurements and both residual exposures
named is the better trade today. It is recorded below rather than closed.

**Leave Windows refused and tell the consumer to use an external
coordinator.** That is the status quo, and it is what produced a downstream
reimplementation of `LockFileEx` with none of these measurements behind it. The
SDK does not get to call a gap conservative when the consumer's only route
around it is to write the same code with less evidence.

## Deferred

- **A DACL check for `checkDir` on Windows.** The argument above is about cost,
  not about correctness, and the cost falls if the SDK ever needs a Windows
  security-descriptor reader for a second reason. Whoever writes it must make
  the *refusing* direction the tested one: an ACL walk that accepts everything
  is indistinguishable from today's no-op and would pass its own tests.
- **Refusing a reparse point at the lock file's path.** On a host where
  `SeCreateSymbolicLinkPrivilege` is available to non-admins, a planted
  junction or symlink redirects a lock name to a file of the attacker's
  choosing — merging two locks into one, or splitting one into two. `os.Lstat`
  sees it; nothing in this package looks. It is deliberately not refused here
  because the Unix side has no counterpart check and adding one on a single
  platform would make the same configuration succeed on Linux and fail on
  Windows for a reason the error could not explain well.
- **A shared (read) lock mode.** `LockFileEx` without
  `LOCKFILE_EXCLUSIVE_LOCK` is a shared lock and `flock(2)` has `LOCK_SH`, so
  both kernels could support one. The `Locker` port is frozen at two methods
  (ADR 0052 §D1) and a shared lease is a different contract, not a flag — it
  would arrive as an ADR 0039 sibling or not at all.
- **`nosleep_external_test.go`'s workspace walk does not terminate on a
  Windows volume root.** `for dir := cwd; dir != "/" && dir != ""; dir =
  filepath.Dir(dir)` never leaves `C:\`, because `filepath.Dir("C:\\")` is
  `C:\`. It is unreachable on the lane that runs it — `go.work` is found two
  levels up — so it is recorded rather than fixed in a change about locking.

## References

- [ADR 0052](0052-sdk-lock-domain.md) — the lock domain; §D7 is the `flock(2)`
  measurement this one is the counterpart to, §D8 the refusal it amends
- [ADR 0018](0018-sdk-cross-platform-portability.md) — the build bar, the
  runtime bar, the `x/sys` ban and the hand-cited-ABI discipline
- `internal/service/lock/flock_windows.go` — the backend and every difference,
  stated at the top
- `internal/service/lock/dirsafety_windows.go` — the directory verdict and the
  two measurements behind it
- `.github/workflows/e2e-cross.yml` — the `windows` job, the only lane that
  runs any of this
- Windows API: `LockFileEx` / `UnlockFileEx` (`winbase.h`), `ERROR_LOCK_VIOLATION`
  (`winerror.h`), `CreateFileW` share modes (`fileapi.h`)
- Go 1.27 `src/syscall/syscall_windows.go` `func Open` — the share mode that
  makes the Windows directory verdict defensible
