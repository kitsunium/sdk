<!-- updated: 2026-10-05T00:00:00Z -->
# internal/service/security/session/

> Reference moved to `internal/service/security/session/NOTES.md` (ADR 0253), under the same section titles: Contents, The two stores, Platform matrix — what the file store needs, and where it refuses, The location is a path, and other accounts may write parts of it, Conventions.

## Purpose

The concrete half of the session domain (ADR 0045): two stores implementing
`internal/core/security/session.Store` — one in process memory, one on disk — and the
AEAD `Sealer` that renders an identifier as a cookie value. Composes
`internal/core/crypto` (AES-256-GCM), `internal/kernel/clock`,
`internal/kernel/fs/pathchain` and `internal/kernel/fs/flock` (the store-wide
lock, which this package and the lock domain each used to carry a copy of —
ADR 0159); it reimplements none of them.

Codes: none declared here. The engines' own refusals (`0.3.46.*`, allocated
here — ADR 0045) and the port's verdicts (`0.2.14.*`) are both declared in
`internal/core/security/session` (ADR 0160) and only raised here, through
`wrapAs`; the `0.3.46.*` values were kept when their declarations moved. The
file store also reuses `internal/core/proc.UnsupportedPlatform` (`0.2.6.1`)
exactly as ADR 0018 §(a) prescribes.

## The store directory's rule, beside the other four

`app/lock`, `proc/ipc`, `security/secret` and `data/queue` each refuse a
directory by a rule of their own, and the five are compared side by side in
`internal/kernel/fs/CLAUDE.md` §Five directory rules. They share the kernel's
measurements (`pathchain`, `winacl`) and lock (`flock`), and no rule. This
store's, and why it is not a neighbour's:

- **Owner-only, judged on the directory it HOLDS.** Any bit beyond `0700` is
  `DirectoryUnsafe`, read through the `os.Root` every later operation uses,
  and the path must still name that directory (`PathRedirected`). Stricter than
  `lock` and `queue`, which accept a GROUP share because a lock or a queue
  shared between two service accounts is their point; a session record is one
  subject's secret. As strict as `secret` on a group READ bit — and, unlike
  `ipc`, which admits group search, there is no peer check behind this
  directory to make reading it harmless.
- **The one rule that narrows.** A directory this call CREATED is `fchmod`ed
  to `0700`, because `MkdirAll`'s mode is a request a default POSIX ACL can
  widen; an operator's existing directory is refused, never narrowed — the line
  every other domain draws at "never narrow".
- **The path above is `lock`'s rule, verbatim** (ADR 0083): an indirection
  whose holder is world-writable is refused, the sticky bit exempting nothing,
  through `pathchain`, BEFORE anything is created. `plantable` on Unix is
  byte-for-byte `lock`'s, and stays a copy: it is one bit test whose meaning —
  who counts as "anybody" — is a policy this store adopted, and `lock`'s
  Windows half (a DACL question) is one this store refuses the platform before
  it could ask. `ipc` judges the same walk more strictly (a foreign owner, a
  replaceable component); `queue` does not walk the path at all.
- **Ownership is not checked** (§What is NOT closed), where `ipc` checks the
  owner of the directory and of every component an outsider could write.
- **Windows is refused** where `lock` and `queue` read the DACL:
  `winacl.GrantsAnyone` answers "can ANYBODY write?", which is weaker than
  owner-only (§Why Windows is still refused).

## Rotation and the ceiling — the one rule worth reading twice

`window.rotate`:

- **same subject → the ceiling does NOT move.** `createdAt` and
  `absoluteExpiry` are carried across. Otherwise a caller rotating on a timer
  would hold an immortal session and the absolute timeout would be decorative.
- **different subject → both clocks restart.** Anonymous becoming alice, or
  alice becoming bob, is a privilege boundary; the thing that exists afterwards
  is a new session, and giving it the remaining seconds of the browsing before
  it would log a user out moments after they signed in.

Pinned by `TestRotatingDoesNotBuyTime` and
`TestChangingPrincipalRestartsTheClocks`, against both stores.

Two more rules sit on the same call:

- **A subject is bounded at 4 KiB in BOTH stores**, before anything is minted
  (`boundSubject`, `PayloadTooLarge`). The file store's frame cannot read back a
  longer string, and it used to write one anyway: `Regenerate` retired the record
  that worked and the new session loaded as `RECORD_CORRUPT`. The memory store has
  no frame, and bounds it all the same, because a subject one store keeps and the
  other cannot read back would make the port two contracts.
- **A rotation whose old record cannot be UNLINKED withdraws the new one**
  (`withdrawLocked`). The caller receives no new identifier, so the record would
  otherwise stay live and bound to the principal with nobody able to reach it,
  one more per retry. The unlink's failure stays the answer; a withdrawal that
  fails too is attached as a `withdraw` field. Once the unlink has happened,
  a failed flush after it is reported and NOTHING is undone, as for every other
  flush: withdrawing the new record there used to leave neither identifier
  resolving, the state the order of the steps exists to prevent
  (`TestAFlushFailureAfterTheRetirementIsNotUndone`).

## Rules from ADR 0073

Superseded by ADR 0154 (the charter); ADR 0073 stays as the incident's record, and its rules live here.

- **Both waits observe the caller's context.** The cross-process lock is
  `flock(LOCK_NB)` — `internal/kernel/fs/flock.TryLock`, which has no blocking
  call to fall back on — polled on the injected clock (`FileConfig.Poll`,
  `DefaultPoll` 25 ms — the `lock` domain's number; negative refused); the
  in-process gate is a one-slot channel selected against the context, taken
  FIRST, because `flock` on one open file description excludes no goroutine.
- **The context is checked once more after both waits**, before the section: a
  select whose cancellation and acquisition become ready together picks either.
- **A caller who left gets `STORE_UNAVAILABLE`**, its own context error in the
  fields — no new code.
- *Lesson*: a blocking `flock(LOCK_EX)` parks its thread in a syscall no
  cancellation reaches, so a request whose client had hung up kept waiting for a
  lock nobody would read the result of — the opposite answer to the one `lock`
  had already given the same syscall.

## Do NOT

- **Put the identifier in a record, a filename, a log line or an error field.**
  `ID.Digest()` exists for every one of those. `TestNoIdentifierIsEverOnDisk`
  walks the whole directory — filenames included — and fails on a match.
- **Seal the memory store's records.** The key would be in the same address
  space as the plaintext.
- **Drop the mode assertions because "the chmod already succeeded".** It is a
  request on the filesystems this check exists for. Verify the invariant, do not
  assume it.
- **chmod a directory the store did not create.** Refuse it.
- **Implement the Windows path without a way to test it on Windows.** ADR 0018's
  runtime bar is not cleared by a green cross-compile.
- **Make `Sweep` a background goroutine.** A store that chose its own cadence
  would own a goroutine the caller never asked for; driving it is
  `pkg/v1/app/scheduler`'s job.
- **Distinguish the causes of `RecordCorrupt` or `SealInvalid`.** One verdict for
  tampering, truncation, a wrong key and a wrong purpose is what keeps them from
  becoming oracles.
- **Skip the directory flush, or undo a change because its flush failed.** The
  first makes a revocation something a power cut can reverse; the second is a
  second write repairing a durability problem (ADR 0056 D7).
- **Recognise a record file by anything looser than `isDigest`, or decode a
  base64url value without `Strict()`.** Sweep deletes what it recognises, and a
  lenient decoder gives one session several spellings.
- **Open anything by `fileStore.dir` after construction.** Every name resolves
  against `fileStore.root`; a path re-resolved later is one a renamed parent
  can move — measured, before the root was held.
- **Read a record or open the lock file with `os.Root.Open`/`ReadFile` alone.**
  `os.Root` follows a link inside the directory on the caller's behalf. Go
  through `openEntry`/`readEntry`, which look first and prove the handle after.
- **Move `checkChain` after `os.MkdirAll`.** `MkdirAll` follows a planted
  parent, so the audit would refuse the directory only after creating it inside
  the planter's tree.
- **Refuse every link in `Dir`'s path.** It refuses every macOS `t.TempDir()`
  and `/var/run` on Linux — ADR 0018 §(a)'s failure mode, blaming the operator
  for the operating system's layout. The rule is the CONTAINER's mode.
- **Run `plantable`'s mode rule on Windows.** `os.Stat` synthesises `0777` for
  every writable directory there; the `_other` variant fails closed and stays
  unreachable behind the platform refusal.
- **Give a link at a record's name its own code.** It is tampering, and it gets
  tampering's one verdict, `RecordCorrupt`.
- **Declare an error code here** — an `errs.Define` or a `Code*` constant.
  Every code of the domain is declared in `internal/core/security/session`, at
  this package's path in the core (ADR 0160); a new refusal of a store or the
  sealer gets its code and sentinel there, in the range this package already
  owns (`0.3.46.*`), and is raised here through `wrapAs`.

## Verification

```
bazel test --config=race //internal/service/security/session:session_test
# OR
cd internal/service && GOWORK=off go test -race -cover ./security/session
# coverage today: ~92% (the remainder is filesystem error branches that need a
# failing device, plus the _other.go stubs this GOOS does not compile)
```

| File | Covers |
|---|---|
| `store_external_test.go` | the lifecycle, the sliding window, the ceiling ending a continuously-used session, expired-record dropping across a backwards clock step, idempotent `Destroy`, the zero identifier, and `Sweep` — all against both stores |
| `fixation_external_test.go` | the fixation attack end to end, `Save`'s refusal of a forged subject, the ordinary data path, the two rotation lifetime rules, a dead session refusing to be re-authenticated, the 4 KiB subject bound (4096 accepted and read back, 4097 refused with the session untouched) in both stores, and the ADR 0031 constructor refusals |
| `file_cancel_external_test.go` | both waits being left: a cancelled caller parked on the lock poll, the poll ending in ACQUISITION once the holder goes (so "cancellable" is not satisfied by a store that never acquires), and a goroutine cancelled while parked on the in-process gate — in a `synctest` bubble, because the cancel has to happen after it is parked there or the context check at the top of `withLock` answers instead; and a caller gone by the time both are held never running the section (`TestACallerThatLeavesWhileAcquiringDoesNotRunTheSection`) |
| `withlock_internal_test.go` | the in-process gate excluding goroutines that share the store's one `flock` descriptor — which `flock` itself does not, a re-lock of one open file description being a conversion rather than a wait — asserted on observed occupancy, not on a final counter |
| `fsguard_internal_test.go` | the store's platform gate never wider than the kernel lock's (`platformNative` implies `flock.Native`), on every GOOS |
| `file_store_external_test.go` | directory and record modes on disk, the operator-owned refusal, no identifier anywhere on disk, filename binding via AAD, tamper/truncation/foreign-key refusal, survival across a reopen, the failed-publish invariant checked byte-for-byte (a failure at temp creation), a sweep that leaves foreign `*.session` files alone, and context cancellation. The rename-onto-a-directory test fails at `Save`'s read and never reaches the rename — its doc says so |
| `pathsafety_external_test.go` | the four planted-path attacks of §The location, each refused: a link at a record's name inside and outside the directory (`RecordCorrupt`, never read, the LINK swept), a link at the lock file dangling or not, inside or out (`PathRedirected`, nothing created through it), a link at a component of `Dir` over the whole container table — 1777, 0777, at a parent and at `Dir` itself refused, 0770 and 0755 honoured — and a parent swapped after construction moving nothing; plus a FIFO or a directory at a record's or the lock file's name, reported and never opened. Tagged like the store; every mutation named in its doc comments was run |
| `pathsafety_internal_test.go` | the branches only a race reaches, driven with the swap already made: `assertHeldDir` against a swapped and a vanished `Dir` and a wide held directory; `sameEntry` against a swapped name, a created name that became a link or vanished, and a handle on a directory; `isLinkNow` after `os.Root` refuses an escaping link |
| `dirsync_internal_test.go` | the directory flush after every rename and unlink, observed through `syncDir` (after the change, once per sweep, again on a retried `Destroy`); a failed flush reported and not rolled back; a failed rotation withdrawing the record it published when the old record's unlink fails, and undoing nothing when only the flush after it does; and the orphan cleanup behind a rename that really fails, over a temporary that was written, synced and closed |
| `sealer_external_test.go` | round trip, cookie-safety, nonce freshness, the seven non-oracle failures, one spelling per sealed value, the empty-purpose refusal, and that opening is not authorising |
| `entropy_internal_test.go` | the collision guard on `New` **and** on `Regenerate`, and the refusal to mint from partial entropy |
| `encode_internal_test.go` | frame determinism, round trip, nine malformed frames (a panic reported as a failure), and the payload caps. Two of the frames — a `0xffffffff` string length, and a `0xffffffff` entry count with nothing after it — can only fail where `int` is 32 bits: run them with `GOARCH=386 go test`, which CI's `test-386` job now does on every PR |
