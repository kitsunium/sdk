<!-- updated: 2026-10-03T03:00:00Z -->
# internal/service/security/session/

## Purpose

The concrete half of the session domain (ADR 0045): two stores implementing
`internal/core/security/session.Store` — one in process memory, one on disk — and the
AEAD `Sealer` that renders an identifier as a cookie value. Composes
`internal/core/crypto` (AES-256-GCM), `internal/kernel/clock`,
`internal/kernel/fs/pathchain` and `internal/kernel/fs/flock` (the store-wide
lock, which this package and the lock domain each used to carry a copy of —
ADR 0159); it reimplements none of them.

Code range: `0.3.46.*` (ADR 0045). The stores also emit the core sentinels
`0.2.14.*`, and the file store reuses `internal/core/proc.UnsupportedPlatform`
(`0.2.6.1`) exactly as ADR 0018 §(a) prescribes.

## Contents

| File | Surface |
|---|---|
| `session.go` | package doc + `wrapAs` (origin-wins sentinel + cause as a field) + the `aesgcm` blank import |
| `config.go` | `Config` (memory) + `validateWindow` — the shared ADR 0031 refusals |
| `file_config.go` | `FileConfig` (+ `Dir`, `Key`) + its `validate` |
| `window.go` | `window` — the deadline policy: `slide` / `deadline` / `live` / `mint` / `rotate` / `build` |
| `record.go` | `record` — what a store keeps. Holds `ID.Digest`, never the identifier |
| `encode.go` | the deterministic at-rest frame + `boundPayload`'s caps |
| `mint.go` | `mintID` — `io.ReadFull` over the injected random source |
| `compare.go` | `digestsEqual` — `crypto/subtle` |
| `memory_store.go` / `memory_write.go` | `memoryStore` |
| `file_store.go` / `file_ops.go` / `file_write.go` / `file_publish.go` | `fileStore`: construction (`openStoreDir` → the held `os.Root`, `assertHeldDir`, `openLockFile`), the record path, publication |
| `chain.go` | `checkChain` — the components ABOVE the store directory, audited through `pathchain` before anything is created (§The location) |
| `file_entry.go` | `openEntry` / `readEntry` — the look before every open and the proof after it, so nothing is read or locked through a link (§The location) |
| `fsguard_unix.go` / `fsguard_other.go` | the platform gate `platformNative` — narrower than `flock.Native`, since Windows has the kernel's lock and not the owner-only modes or the directory flush — `plantable` (the mode rule, Unix only), and the honest refusal, with the reason Windows is refused, corrected. The lock itself is `internal/kernel/fs/flock`, polled `LOCK_NB` by `file_ops.go` (ADR 0073) |
| `sealer.go` | `sealer` + `NewSealer` |
| `codes.go` / `errors.go` | `RecordCorrupt` / `DirectoryUnsafe` / `LockFailed` / `PayloadTooLarge` / `InvalidPurpose` / `PathRedirected` (`0.3.46.6`) |

## The two stores

| | `NewMemoryStore` | `NewFileStore` |
|---|---|---|
| Survives a restart | no | yes |
| Scope | one process | one host, one directory |
| Records sealed at rest | no — see below | yes, AES-256-GCM |
| Honours `ctx` | no — it never blocks | yes, and BOTH waits can be left (ADR 0073) |
| Cross-process safe | n/a | yes, one exclusive `flock` per operation |
| Bound to the directory it checked | n/a | yes — audited for planted links, then HELD as an `os.Root` (§The location) |
| Available everywhere | yes | linux/darwin/freebsd/openbsd/netbsd/dragonfly only |

The memory store does **not** seal its records, and that is not an oversight:
sealing in-process memory with a key held in the same process protects against
nothing. Anything that can read the map can read the key.

## Platform matrix — what the file store needs, and where it refuses

The file store makes the guarantees listed in full on `fileStore`. Four of them
rest on operating-system mechanics, and only one of those is portable:

| Mechanic | Needed for | Portable? |
|---|---|---|
| `rename(2)` / `MoveFileEx` | atomic publication | **yes**, everywhere Go runs |
| `os.Root` | one directory held for the store's lifetime | **yes** — the standard library's own `openat` walk |
| enforced Unix permissions | owner-only records, and the `plantable` verdict on a link in `Dir`'s path | no |
| `flock(2)` — `internal/kernel/fs/flock` | serialised read-modify-write | no — the kernel's primitive has it on the six Unix kernels and `LockFileEx` on Windows, nothing on illumos/Solaris; `TestTheStoreIsBuiltOnlyWhereTheKernelCanLock` pins that this store never builds where it has none |
| `fsync(2)` on the directory | a rename or unlink that survives a power cut | no — Windows has no directory flush (ADR 0056 D10), and the store is already refused there |

Where either of the last two is missing, `NewFileStore` returns
`proc.UnsupportedPlatform` **at construction** — not per operation, so the
refusal arrives where the program is wired rather than at the first login.

| Platform | Verdict |
|---|---|
| linux, darwin, freebsd, openbsd, netbsd, dragonfly | native |
| **windows** | `UnsupportedPlatform` — see §Why Windows is still refused. Not because stdlib `syscall` cannot reach `advapi32` (the reason this row used to give, stale since ADR 0081/0084), but because three things are missing: an owner-only DACL that is BUILT and verified, a directory flush, and a lane that runs the package there |
| wasip1, solaris, illumos, aix | `UnsupportedPlatform` — `syscall.Flock` is absent from the stdlib there, or file ownership means nothing |
| plan9, js | outside the SDK's build matrix entirely, and not because of this package: `internal/core/proc` does not compile on either (`syscall.Note` on plan9, no signal constants on js). Measured, pre-existing, and unchanged by this domain — `internal/service/security/session` itself compiles on both `GOOS` values in isolation |

Both `fsguard_*.go` files compile on every `GOOS` the SDK targets, so the
package always clears ADR 0018's **build bar**; only behaviour degrades. Verified
by cross-compiling the package for all seven matrix platforms plus wasip1,
solaris, illumos, android and ios.

### Why Windows is still refused

This row used to say the right DACL needs `CreateFileW` with a security
descriptor "which stdlib `syscall` does not expose". That stopped being true
with ADR 0081 and ADR 0084: the kernel binds `LockFileEx` from `kernel32`
(`internal/kernel/fs/flock`) and `GetNamedSecurityInfoW` + `GetAce` from
`advapi32` (`internal/kernel/fs/winacl`) through `syscall.NewLazyDLL`, no new
dependency — code the lock domain wrote and ADR 0159 moved down to the kernel.
Reusing it does not get this store there, because three things are still
missing:

| Missing | Why the kernel's primitives do not supply it |
|---|---|
| an owner-only DACL, BUILT and then verified | the kernel's reader (`winacl.GrantsAnyone`, ADR 0084/0086/0095) builds nothing, and answers a weaker question: does an identifier meaning ANYBODY — Everyone, Authenticated Users, BUILTIN\Users — hold a right. `0700`/`0600` exclude every other account, a named colleague included; a directory granting read to one named principal passes the reader and fails the Unix rule. Applying a protected owner-only DACL at creation is new ABI (`SetNamedSecurityInfoW`, or a `SECURITY_ATTRIBUTES` on the create) with its own tests |
| a directory flush | none exists: `FlushFileBuffers` on a directory handle returns `ERROR_ACCESS_DENIED` (ADR 0056 D10) — the reason `internal/service/data/vfs` refuses Windows too. Without it a power cut can undo a `Destroy` |
| a lane that runs it | no Windows job runs this package; a green cross-compile is not ADR 0018's runtime bar |

The kernel's `LockFileEx` (ADR 0081, `internal/kernel/fs/flock`) is the one
piece that exists, and it is not enough on its own. `GOOS=windows go vet ./internal/service/security/session/...` is clean, which
is the build bar and only that.

### Requesting a mode is not getting one

Every permission the store depends on is **narrowed and then asserted**, because
three different things can silently widen it:

- a **default POSIX ACL** on the parent makes `MkdirAll(0700)` and the record
  temporary's `0600` come back wider — this happens on this repository's own
  devcontainer, where `t.TempDir()` yields `0775`;
- a **filesystem that does not implement Unix permissions** (exFAT, SMB, a
  container mount with a blanket `file_mode=`) accepts the `chmod` and changes
  nothing;
- an operator's **pre-existing directory** may simply be `0755`.

So: `chmod` what we created, refuse what we did not, and `stat` either way —
both through the handle of the directory the store HOLDS, so the directory
narrowed and judged is the one every later operation uses. A directory the
store creates is narrowed to `0700`; a directory that already existed is
**refused** with `DirectoryUnsafe` rather than chmod'ed, because narrowing an
operator's directory — possibly shared with another service — is not the SDK's
decision to make. Records are created, chmod'ed to `0600`, and stat'ed before a
byte is written to them.

### Why one store-wide lock

Per-record locking needs a lock file per record, and a lock file that is ever
unlinked has a well-known race: a process blocked on the old inode acquires it
just as another creates and locks a new one, leaving two holders. Never
unlinking them leaks a file per session. One descriptor, opened at construction
and held for the store's lifetime, avoids both — and is released by `Close`,
which is `io.Closer` and therefore needed no new interface (ADR 0039).

`Load` takes the **exclusive** lock, not a shared one, because `Load` writes: it
slides the idle window. That is the cost of a sliding expiry on a persistent
store, and it is stated rather than hidden.

Neither wait for it blocks unabandonably (ADR 0073). `flock` is `LOCK_NB` plus a
poll on the injected clock — a blocking `LOCK_EX` parks the thread inside a
syscall no cancellation reaches, so a request whose client hung up kept waiting
for a lock nobody would read the result of — and the in-process gate is a
one-slot channel rather than a `sync.Mutex`, whose `Lock` cannot be told its
caller has gone. A caller who leaves gets `STORE_UNAVAILABLE` with its own
context error in the fields. `withLock` checks the context once more after both
are held, before the section runs: every wait is a select, and a select whose
cancellation and acquisition become ready together picks either at random.

### Every rename and every unlink is flushed

POSIX does not make a rename or an unlink survive a crash until the containing
directory is flushed, so without it a power cut after `Destroy` may legally bring
the destroyed record back — a revocation undone. Every publication
(`writeLocked`) and every removal (`removeLocked`) is therefore followed by an
`fsync` of the directory (`flushLocked`), after the change, never before it.
Three rules come with it:

- **A failed flush is `StoreUnavailable`, and it is NOT rolled back.** The change
  is already visible to every reader; reverting a rename to repair a durability
  problem is a second write that can fail the same way (ADR 0056 D7). The retry
  is what makes it durable — which is why removing an already-absent record still
  flushes: a retried `Destroy` must be able to finish the revocation whose flush
  failed.
- **A sweep flushes once per pass**, not once per record: it unlinks every dead
  record under the lock (`unlinkLocked`) and then flushes the directory a single
  time, so a large sweep costs one device round trip instead of one per session.
- **The flush is a field** (`fileStore.syncDir`, `flushHeld` over the held root in production)
  so `dirsync_internal_test.go` can observe *when* it runs and make it fail. A
  failing directory `fsync` cannot be provoked on a real filesystem.

## The location is a path, and other accounts may write parts of it

Four attacks, each first run against the store as it shipped (darwin/arm64,
`390aa80f`) and each now refused. The mechanisms are the lock domain's and
`vfs`'s, not new ones:

| Planted | Before | Now | Mechanism |
|---|---|---|---|
| a link at a record's name, to a copy kept elsewhere | `Load` served the copy (`where="outside"`) | `RecordCorrupt`, never read; a sweep unlinks the LINK, never its target | look first, prove the handle (`file_entry.go`) |
| a dangling link at `.lock` | store built; the link's target created and flocked | `PathRedirected` (`kind=symlink`); nothing created | the same, at construction |
| a link at a component of `Dir`, in a 1777 directory | store built; every record under the planter's tree | `PathRedirected`, naming the component and its target; nothing created | `pathchain` before `MkdirAll`, with lock's rule (`chain.go`) |
| `Dir` renamed away after construction and replaced (a 0777 parent, no link anywhere) | records followed the path into the replacement | records stay in the directory the store checked | the directory is HELD as an `os.Root` |

Writes were never redirected: `rename(2)` replaces a link at its destination
rather than following it, and the temporary is created `O_EXCL`, which never
follows.

- **The parent-chain rule is ADR 0083's**, read off the same
  `pathchain.StepValue.Container`: an indirection is refused when the directory
  holding it is world-writable, and the sticky bit exempts nothing, because
  planting creates an entry. A link in a directory only its owner (or its
  group) can write is honoured — `/tmp` on macOS, `/var/run` on Linux, and every
  macOS `t.TempDir()` (`/var -> /private/var`) are links like that. Both halves
  are pinned by one table.
- **Held, not re-resolved.** The audit sees the path once; the handle keeps it
  true afterwards. Every name — records, temporaries, the lock file, the sweep's
  listing, the directory flush — resolves against the `os.Root` opened at
  construction, after `assertHeldDir` proved the held directory is owner-only
  and is still the one `Dir` names. That is the half the lock domain leaves open
  (it re-resolves its path at every open, so a component replaced after
  `checkChain` is not seen, as ADR 0083 says); here it moves nothing.
- **Look, then prove — because `os.Root` has no `O_NOFOLLOW`.** It ORs the flag
  in itself and then resolves the link on the caller's behalf, inside the root
  (go1.27 `src/os/root_unix.go`), and `syscall.Openat` exists for linux, aix and
  wasip1 only (ADR 0083). So `openEntry` `Lstat`s the name through the held
  directory and refuses a link — or anything that is not a regular file —
  before any open, then compares the opened handle with what the name was
  (`os.SameFile` on its `fstat`). Nothing is read from, or locked on, a handle
  that fails. What remains is a follow INSIDE the store directory during that
  race, by an account that can already write a 0700 directory it does not own.
- **A link at a record's name is `RecordCorrupt`**, the one verdict for every
  record this store did not write: no oracle, and the 401 that lets a client
  start over. A directory or a FIFO there is `StoreUnavailable`
  (`kind=not-regular`), as reading a directory always answered — but it is
  never opened, so a FIFO cannot park a `Load` inside the store-wide lock.

### What is NOT closed

- **Ownership is not checked, and neither is an ACL.** `assertHeldDir` reads
  mode bits, as the check it replaced did. A store running as root over a
  `0700` directory another account owns is accepted, and so is one whose
  extended ACL (macOS, NFSv4) grants another account what the mode bits do not
  show; either account can then plant, rename and restore entries. An owner
  check would close the first, and is not part of this change because it would
  also refuse a root process over a service account's directory — a deployment
  shape nobody has measured.
- **A world-writable, non-sticky ancestor that holds no link** is accepted, as
  the lock domain accepts it. With the directory held, a rename there after
  construction no longer matters. Before construction its planter can create
  `Dir` itself: refused when its mode shows another account, unopenable when it
  is the planter's `0700` — unless the store runs as root or an ACL lets it in
  (the bullet above).

### Why publication is not `vfs.WriteAtomic`

The audit that found this asked whether the publication copy here could be
`internal/service/data/vfs`'s, which `internal/service/security/secret` uses. Not without
dropping what §Do NOT forbids dropping:

| This store needs | `vfs` offers |
|---|---|
| the record temporary narrowed and `stat`'ed **before its first byte** (`DirectoryUnsafe`) | `WriteAtomic` creates with the mode and asserts nothing |
| a directory flush after every **unlink**, which is what makes `Destroy` durable | `Remove` does not flush, and the port has no flush verb |
| an `Lstat`, and a handle on the directory, for the lock file and the record reads | `FullFS` has neither (`fs.ReadLinkFS` is not implemented) |
| the flush observed and made to fail (`dirsync_internal_test.go`) | `atomicOps` is unexported |

So the five steps stay here, now over the held root. What would let them go is
a change to `vfs` itself — the mode assertion in `WriteAtomic`, a durable
remove or flush sibling (ADR 0039), and `fs.ReadLinkFS` — which changes `vfs`
for every caller and is not this change.

## Conventions

- **Nothing waits on the wall clock, and nothing sleeps.** The memory store
  takes `clock.Clock` — the *narrow* half (ADR 0039), since it only reads time.
  The file store takes `clock.Timed`, because it also WAITS: the poll between
  lock attempts is armed on it (ADR 0073), which is what makes contention
  assertable without sleeping. Every expiry assertion in the suite advances a
  `ManualClock`, and so does every contention one.
- **Every contract test runs against BOTH stores** from one table
  (`store_external_test.go`'s `factories`). A contract only one store honours is
  not a contract, and the fixation guard in particular has to be identical in a
  store that persists and one that does not.
- **The random source is a field, never a `Config` knob.** A public knob letting
  a caller swap the source of every session identifier is a footgun with no
  legitimate production use. It is reachable from `entropy_internal_test.go`,
  which is why the collision guard is asserted rather than argued.
- **`wrapAs` puts the sentinel at the origin and the cause in a field.** A
  filesystem error must not be able to hijack the code a framework routes on.
  The trade is explicit: `errors.Is(err, fs.ErrPermission)` does not work, and
  the OS message stays reachable through `errs.FieldsOf`. It never contains an
  identifier, because files are named by digest.
- **The at-rest frame is hand-written, not dispatched through `codec`.** It is
  storage, not interchange: nothing outside this package reads it, it must be
  total over `map[string]string` with no reflection and no tag vocabulary, and
  routing it through the registry would drag a codec into every binary that
  wants a session store. It is deterministic (sorted keys), versioned, and
  bounded before it allocates — on the `uint32` the frame carries, converted to
  `int` only once it is proven small, because where `int` is 32 bits
  `int(0xffffffff)` is `-1` and passed every "> cap" check.
- **A record file is recognised by one test, `isDigest`** — 64 lowercase hex
  characters, exactly what `ID.Digest` writes — and `recordName` and
  `recordDigest` both apply it. What a sweep recognises is what it deletes, and a
  length-only test swept any foreign file that merely had a 64-character stem.
- **A sealed value has one spelling.** `Open` decodes with the STRICT base64url
  encoding, so a respelling through the unused bits of the last character is
  refused; the AEAD cannot catch it, because the bytes it authenticates are
  identical. `core/security/session.ParseID` holds the identifier to the same rule.

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
