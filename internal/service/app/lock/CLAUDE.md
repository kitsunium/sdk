<!-- updated: 2026-10-04T10:00:00Z -->
# internal/service/app/lock/

## Purpose

The concrete lockers implementing `internal/core/app/lock` (**ADR 0052**):

- **`NewMemory`** — excludes the goroutines of one process. Its leases **expire**.
- **`NewFileLocker`** — excludes the processes sharing one directory on one
  machine, through `flock(2)` on Unix and `LockFileEx` on Windows (ADR 0081) —
  the kernel's `internal/kernel/fs/flock`, which session's file store takes
  too (ADR 0159). Its leases **do not expire**.
- **`Keepalive`** — renews a lease in the background and **cancels a derived
  context** the instant the lease is lost.

Stdlib-only. Code range: `0.3.51.*`, plus the core sentinels `0.2.21.*` —
both declared in `internal/core/app/lock` (ADR 0160); this package declares no
code and raises the core's.

Composes `internal/kernel/clock`, `internal/kernel/fs/pathchain`,
`internal/kernel/fs/flock` and, on Windows, `internal/kernel/fs/winacl` — the
DACL reader this package wrote and the queue now shares from there.

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

## Rules from ADR 0082

Superseded by ADR 0154 (the charter); ADR 0082 stays as the incident's record, and its rules live here.

- **The lock path is a file, never a link to one.** Unix opens it with
  `O_NOFOLLOW` and the kernel refuses; Windows opens it with
  `FILE_FLAG_OPEN_REPARSE_POINT` and refuses a handle carrying
  `FILE_ATTRIBUTE_REPARSE_POINT` — the flag alone opens the link, the pair
  refuses it. A platform gains the lock and its hardening together or neither:
  `nofollow_*.go` and the kernel lock's `flock_*.go`
  (`internal/kernel/fs/flock`) share their build tags, and since they sit in
  two packages `TestTheLockAndItsHardeningShareAPlatform` is what keeps them
  equal; `platformNative` needs both.
- **A planted indirection is `LOCK_PATH_REDIRECTED`** (`0.3.51.4`, exit 78),
  never `LOCK_BACKEND_FAILED`, which invites the one wrong response — a retry.
- **The errno is never branched on** (`ELOOP`, `EMLINK`, `EFTYPE` across six
  kernels); the `Lstat` after a failed open is diagnosis, the kernel having
  already decided.
- *Lesson*: the lock filename is predictable and the directory rule governs
  unlinking, while the attack creates; over a planted link the shipped locker
  returned `held=true` with no error, its `flock` and fencing ledger outside the
  checked directory.

## Rules from ADR 0083

Superseded by ADR 0154 (the charter); ADR 0083 stays as the incident's record, and its rules live here.

- **Every component of the lock directory is audited before `MkdirAll`**,
  through `pathchain`. An indirection at a parent is refused
  (`LOCK_PATH_REDIRECTED`) only when the directory holding it is open to
  anybody — world-writable on Unix, the sticky bit exempting nothing because
  planting creates an entry; on Windows, `winacl.CreateRights` in its DACL
  (ADR 0086).
  The operating system's own links (`/var` → `/private/var`, `/var/run`) pass.
- **A held lock can lose its file, and the holder is told.** In a
  `0777|sticky` directory the entry's owner may unlink a held lock file and
  nothing prevents it. `Acquire` and `Extend` compare the descriptor with the
  name (`os.SameFile` over `Lstat`, failing open) and answer
  `LOCK_FILE_REPLACED` (`0.3.51.5`); a `Keepalive` cancels. A test asserts the
  second holder still acquires: detection, never prevention — the prevention is
  a directory no other account can write, which `NewFileLocker` creates `0700`.
- *Lesson*: `O_NOFOLLOW` governs the final component only, so a link at a parent
  moved the whole lock directory; and an unlinked lock file left two holders on
  two inodes, the fence reset to 1 and the victim's `Extend` returning nil.

## Rules from ADR 0084

Superseded by ADR 0154 (the charter); ADR 0084 stays as the incident's record, and its rules live here.

- **On Windows the directory question is asked of the DACL, never of a mode**
  (`os.Stat` synthesises `0777` for every writable directory):
  `GetNamedSecurityInfoW` and `GetAce` from `advapi32` through
  `syscall.NewLazyDLL`, no `x/sys` — the reader is `internal/kernel/fs/winacl`
  now, and its own rules (the mask is read, denials per account, the ACE type
  before its identifier, a NULL DACL as the most permissive list) live in its
  `CLAUDE.md`.
- **A directory this process created is checked like any other** — on Windows
  it inherits its parent's list and `lockDirMode` means nothing.
- **An API that will not answer fails OPEN**, its Win32 status in a field; an
  answer in hand is never fail-open.
- *Lesson*: `checkDir` accepted every Windows directory and `checkChain`
  refused nothing there — a gap estimated at 250 lines that cost two exports.

## Rules from ADR 0086

Superseded by ADR 0154 (the charter); ADR 0086 stays as the incident's record, and its rules live here.

- **Creating an entry is not replacing one, and three masks say which**:
  `winacl.ReplaceRights` (`FILE_DELETE_CHILD`, `WRITE_DAC`, `WRITE_OWNER`) for
  `checkDir`; `winacl.CreateRights` (`FILE_ADD_SUBDIRECTORY`, `WRITE_DAC`,
  `WRITE_OWNER`) for a planted component; `winacl.ContentRights` for what the
  directory's new files INHERIT — so an `INHERIT_ONLY_ACE` counts.
- **"Anybody" is Everyone, Authenticated Users and `BUILTIN\Users`**:
  `%ProgramData%` (create, not replace) is accepted, a junction planted beside
  it refused.
- **Every discretionary ACE shape is decoded**, bounded by `AceSize`; the
  directory and its files keep separate denial states; the SACL is never read —
  the reader's half of ADR 0086, kept with the reader in
  `internal/kernel/fs/winacl/CLAUDE.md`.
- *Lesson*: one mask for two questions accepted a directory whose new lock files
  every account could rewrite — and the fencing ledger with them.

## Do NOT

- **Share one descriptor across acquisitions.** See §`flock(2)` excludes
  nothing. It is the efficient-looking design, it silently removes goroutine
  exclusion on Unix, and it makes every second acquisition fail outright on
  Windows — wrong twice, visible once.
- **Give `nameGate` a TTL.** It would desynchronise from the `flock` it guards.
- **Replace the poll with a blocking lock** — a blocking `flock`, or
  `LockFileEx` without `LOCKFILE_FAIL_IMMEDIATELY`, which is the same mistake
  spelled differently. The kernel primitive (`internal/kernel/fs/flock`)
  deliberately has no blocking call; do not wrap one around it here.
  `Acquire`'s context would stop meaning anything.
- **Run the POSIX directory rule on Windows.** It refuses every directory, and
  `prepareDir` only checks directories it did not create — so the symptom is a
  program that starts once on a fresh machine and never again.
- **Fork the DACL reader, here or for another package.** It is
  `internal/kernel/fs/winacl` so that there is one: this package and
  `internal/service/data/queue` ask it masks of their own. A new question is a
  new pair of masks, not a new walk — and the reader's own Do NOT list (a NULL
  DACL is not "no grants", the mask is read and not just the SID, one denial
  state per object, no ACE shape skipped, no SACL) lives in its `CLAUDE.md`.
- **Make the DACL check refuse on an API failure.** It fails OPEN on purpose:
  this code cannot be iterated locally, a wrong refusal costs a locker that
  never builds on a safe directory, and a wrong acceptance leaves the platform
  where it already was. Those are not symmetric (ADR 0084 §D5). The QUEUE,
  asking the same reader, refuses on the same failure, because its asymmetry
  runs the other way (ADR 0095) — which is the caller's call, not the
  reader's. So the reader decides nothing and must keep naming every
  no-verdict answer: an empty `observed` means "read to the end", only that.
- **Return a non-empty `observed` from `plantable` when the verdict is
  "safe".** An empty `observed` is what tells `checkChain` a verdict was
  REACHED; filling it in on the safe path would make every component look
  uninspected and log on the ordinary one.
- **Give `checkDir` and `plantable` the same mask.** They ask different
  questions, on both kernels. Merging them is what kept `BUILTIN\Users` out of
  `anyoneSids` for a whole ADR, because one mask cannot accept `%ProgramData%`
  and refuse `%SystemRoot%\Temp` (ADR 0086 §D1).
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
  The tag sets of the kernel lock's `flock_*.go` (`internal/kernel/fs/flock`)
  and this package's `nofollow_*.go` are identical on purpose — a platform
  gains a lock and its hardening together or gains neither — and
  `TestTheLockAndItsHardeningShareAPlatform` fails on the GOOS where they part.
- **Make `Keepalive` release the lease on stop.** The lifetime of a lock must
  not depend on the lifetime of a convenience.
- **Add a distributed backend here.** Redis / etcd / Consul are connectors to
  third-party systems and belong under `third-party/` (ADR 0052 §D11).

## Verification

```
bazel test --config=race //internal/service/app/lock:lock_test
# or: cd internal/service && GOWORK=off go test -race ./app/lock/...
```

## Reference in NOTES.md

Moved unchanged to `internal/service/app/lock/NOTES.md`, under the same titles, and read only by a change that needs its mechanics: `The two backends are not two speeds`; ``flock(2)` excludes nothing between goroutines — measured`; `Contents`; `Platform matrix (ADR 0018)`; ``LockFileEx` is a different primitive, and the differences are meas...`; `The lock directory's verdict is platform-specific, rule AND reason`; `The lock directory's rule, beside the other four`; `The directory rule never reached the lock file's NAME`; `The two things ADR 0082 left open, and which of them is PREVENTED`; `A link at a PARENT component — prevented`; `Unlink-and-replace in a sticky directory — DETECTED, not prevented`; `Sentinels`.
