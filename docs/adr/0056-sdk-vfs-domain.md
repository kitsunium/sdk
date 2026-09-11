# ADR 0056 — filesystem domain (`vfs`), and exactly how far "atomic" reaches

- **Status**: Accepted
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Related**: [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published port grows by siblings), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (refuse or clamp, never inert), [ADR 0018](0018-sdk-cross-platform-portability.md) (refuse where the mechanic does not exist), [ADR 0045](0045-sdk-session-domain.md) (the file store this domain generalises), [ADR 0030](0030-stdout-is-a-protocol-channel.md) (a zero value must not be the dangerous one)

## Context

Three packages in this repository already publish a file by hand.
`internal/service/session/file_publish.go` writes a sealed record beside its
target and renames it. The docs site generator writes a tree and swaps it. The
graph publisher does the same thing again. Each one re-derives the same
sequence, and each one gets a slightly different amount of it right — the
session store flushes the file and the directory, the others flush neither.

"Generate beside, swap by `rename(2)`, flush the parent" is an invariant the SDK
repeats by hand, and **a discipline repeated by hand is a discipline that will
be forgotten exactly once**. The interesting part is that forgetting it is
invisible in every test: a publisher that writes in place passes its unit tests,
passes its integration tests, and fails only when a reader happens to be reading
during the write — which is a scheduling accident, not a code path.

So the question this ADR answers is not "should the SDK have a filesystem
abstraction". It is: **what precisely does atomic publication guarantee, on
what, and where does the guarantee stop?** A primitive that promises more than
it delivers is worse than no primitive, because it retires the caller's caution.

### The measurement that motivates the whole domain

`TestOnlyTheAtomicWriterSurvivesAConcurrentReader` runs one writer against one
reader over a 64 KiB file, 300 publications, 600 reads, and counts the reads
that observed neither the complete old version nor the complete new one. The
same harness, the same payload, the same loop — the only variable is the verb:

| verb | torn reads out of 600 |
|---|---|
| `WriteFile` (in place) | **540 – 597** across runs (**203** under `-race`) |
| `WriteAtomic` | **0**, every run |

**Between 90 % and 99.5 % of concurrent reads of an in-place write saw a broken
file.** This is not a rare race that a careful reviewer might reason away; on
this machine it is the common case. That number is the argument for the domain.

## Decision

Admit `vfs` as the 18th core sibling: a port in `internal/core/vfs`, two
implementations in `internal/service/vfs`, a facade in `pkg/v1/vfs`. Code blocks
`0.2.25.*` (port) and `0.3.55.*` (implementations).

### D1 — reading is `io/fs`, and the SDK does not restate it

`vfs.FS` is a **type alias** of `io/fs.FS`, and `WritableFS` embeds it. One
line, and every filesystem from this domain *is* an `fs.FS`: `fs.WalkDir`,
`fs.Glob`, `fs.ReadFile`, `fs.Sub`, `fs.Stat` and every third-party consumer of
`fs.FS` work on it unchanged and with no adapter.

There is deliberately **no `vfs.Walk` and no `vfs.Glob`**. Adding either would
not be a feature — the stdlib versions are correct, maintained by the Go
project, and already reachable. A second pair would be a second place for a bug
to live, and on the day the two disagreed the SDK's would be the wrong one.

An alias rather than a look-alike interface is load-bearing and is pinned by a
test: redeclaring `FS` as `interface{ Open(string) (fs.File, error) }` keeps
everything inside the SDK compiling and breaks every stdlib walker at the
boundary.

### D2 — `WritableFS` is frozen at four verbs; `AtomicWriter` is a sibling

`WriteFile`, `MkdirAll`, `Remove`, `RemoveAll`, on the embedded `FS`. Publication
is **not** a fifth method: it is `AtomicWriter`, reached by type assertion, per
ADR 0039.

That is not ceremony. `pkg/v1/vfs` aliases these interfaces, Go interfaces are
structural, and a fifth method would break every downstream implementation at
compile time with no deprecation window. It is also *true to the domain*:
publication is a **capability**, and filesystems that genuinely lack it exist —
an object store with no rename, a read-through overlay, a tar archive. `FullFS`
is the union, and it is what the SDK's own constructors return so that a caller
wiring one of them need not assert for the headline feature.

### D3 — the path grammar is `fs.ValidPath`, and it is LEXICAL only

One rule, the standard library's, applied to readers and writers alike — so a
name that can be written can always be read back, and the SDK has not invented a
discrepancy of its own. It refuses the empty string, a leading or trailing
slash, a doubled slash, and any `.` or `..` element. `ValidateWritePath` adds the
one refusal a writer needs: the root itself is not a target, which is what stops
`RemoveAll(".")` from emptying a filesystem while reading, in a diff, exactly
like a no-op.

**A path is refused, never normalised.** Every normaliser is a small parser,
every small parser has a case its author did not think of, and the caller never
learns that the path it asked for is not the path it got.

Two consequences are stated because they surprise people:

- The separator is `/` and nothing else, so `..\..\etc\passwd` is **accepted**
  as one legal, peculiar POSIX filename. It lands inside the root. Rejecting
  backslashes would make a legal filename unwritable to buy a confinement the
  grammar already provides.
- The grammar closes `../../etc/passwd` and **does not** close a symbolic link
  that leaves the tree. Every element of `link/secret` is legal; whether it
  escapes depends on what `link` points at, which is a runtime property of the
  filesystem and not of the string. That is the implementation's half — D8.

### D4 — a zero mode is refused, never defaulted (ADR 0031)

ADR 0031 admits clamping where a working default needs no explanation and
demands a refusal where any SDK-chosen value would be arbitrary. A file mode is
unambiguously the second case: `0644` and `0600` differ by **who may read the
bytes**, the SDK does not know what the bytes are, and a mode of `0` is what an
unfilled struct field looks like. Silently choosing would make the SDK the
author of a security decision it has no information about — the same argument
ADR 0030 makes about a zero value never being the dangerous one.

Bits outside `fs.ModePerm` are refused **by name**, so `0o4755` instead of
`0o755` is a typed error rather than a one-character privilege escalation.

### D5 — publication is five steps, and the order is the contract

1. **Create a temporary in the SAME directory as the target.** Same directory
   means same filesystem by construction, so step 4 cannot fail with `EXDEV`:
   the cross-device rename is not *handled*, it is **unreachable**. The name is
   `.vfs-<16 bytes of hex>.tmp`, created `O_EXCL`, so a collision with a
   concurrent publisher is an error rather than a silent overwrite.
2. **Write the payload.**
3. **Flush the FILE** (`fsync`). A rename over data still sitting in the page
   cache is atomic about nothing once the machine loses power.
4. **Rename.** The indivisible step.
5. **Flush the DIRECTORY** (`fsync` on the directory descriptor). Without it the
   entry naming the new inode may not survive a crash — see D7.

**Steps 1–4 failing mean nothing was published.** The temporary is removed and
the previous bytes are untouched. Returning `PublishFailed` is therefore an
*assertion*, not a description: the caller may read it as "the previous content
is intact and no temporary survives", and so the operation is both safe to retry
and safe to abandon.

That assertion is proved rather than asserted. `publish_internal_test.go`
substitutes mechanics that fail at each step in turn — create, write, sync,
close, rename — over a **real** file handle, so that on the write case the
temporary genuinely is half-written, which is the state a process killed
mid-publication leaves. For each step it hashes the destination before and
after, compares, and lists the directory for orphans. Sabotaging behind an
interface is what makes those paths reachable at all: a write that stops halfway
cannot be provoked on a real file without a hostile filesystem, and **an
untested cleanup is a cleanup that has never run**.

### D6 — what atomic publication guarantees, and what it does NOT

This is the section the ADR exists for.

**It guarantees**, on a local POSIX filesystem where the kernel implements
`rename(2)`:

- A concurrent reader opening the name observes the complete previous content or
  the complete new content, never a mixture, never a truncation, and never a
  name that resolves to nothing. Measured: 0 torn reads out of 600, every run.
- On any failure through step 4, the previous bytes are byte-for-byte unchanged
  and no temporary survives where the caller can see it. Measured by hash, per
  failing step.
- After step 5 returns nil, the content **and its directory entry** have been
  flushed to the device, so the publication survives a crash.

**It does NOT guarantee:**

- **Anything across filesystems.** `rename(2)` returns `EXDEV` between mounts.
  Step 1 makes that unreachable rather than solving it — the domain has no
  copy-and-delete fallback, deliberately, because such a fallback is *not
  atomic* and offering it under the same method name would be the exact lie this
  ADR exists to avoid. A caller publishing across devices must stage the file
  itself.
- **Atomicity where `rename` is not the kernel's.** POSIX requires an atomic
  replace, and local Linux/BSD/macOS filesystems provide it. A **FUSE** driver,
  an **SMB/CIFS** mount, or **FAT/exFAT** implements rename itself and may do it
  as unlink-then-link, which has a window. The SDK cannot detect this and does
  not try: it inherits whatever the mount provides. On **NFS** the rename is
  atomic on the server, but a client attribute cache can still serve a reader
  stale metadata, and an `fsync` reaches the server rather than necessarily the
  server's platter.
- **Durability beyond what `fsync` actually does.** A device that acknowledges a
  flush it has only buffered — a consumer SSD lying about write-through, a
  virtual disk with an unsafe cache mode — loses a published file on power loss.
  No code here can detect that. The SDK's promise is "we called `fsync` on both
  the file and the directory, and checked the result"; it is not "the bytes are
  on a platter".
- **Mutual exclusion between publishers.** There is no locking and none is
  wanted. Two processes publishing the same name concurrently both succeed and
  the winner is whoever renamed last; no reader ever sees a mixture, but no
  ordering is imposed. (The `flock(2)`-excludes-nothing-between-goroutines
  lesson of ADR 0045 does not apply here, because this domain takes no `flock`
  at all.)
- **Anything about the temporary after a crash.** A process killed between steps
  1 and 4 leaves a `.vfs-*.tmp` orphan. It is hidden from an ordinary listing and
  suffixed so whoever finds one knows what it is, but the SDK does not sweep
  them: a sweeper cannot distinguish an abandoned temporary from another
  process's in-flight one without a lock this domain refuses to take.
- **Race-free symlink refusal.** See D8.

### D7 — a directory flush that fails after the rename is a DIFFERENT verdict

Steps 1–4 failing mean nothing was published. Step 5 failing means **the
opposite**, and conflating them is the mistake worth naming.

Once `rename(2)` has returned, every reader sees the new content. The only thing
still in doubt is whether the directory entry survives a power loss. So the
verdict is a distinct code — `DirectorySyncFailed` (`0.3.55.2`) — whose `Public`
says exactly that: *the file was published but the directory entry was not
flushed*. Reporting `PublishFailed` here would assert the previous content is
intact, which is false, and a caller that retried on it would be retrying a
publication that already happened.

**It is deliberately not rolled back.** Undoing it would mean a second,
non-atomic write to repair a durability problem — which is how a good file gets
replaced by a worse one.

What is lost if step 5 is skipped entirely (as the SDK's earlier hand-rolled
publishers did): POSIX does not require a rename to be durable without an
`fsync` of the containing directory. After a crash the filesystem may legally
present the directory as it was before the rename — the old file, intact, which
is safe — **or** a directory entry naming an inode whose data blocks were never
written, which is a file of the right name and the wrong content. ext4's
`auto_da_alloc` heuristic detects the rename-over-an-existing-file pattern and
forces the allocation, which mitigates this on one filesystem on one operating
system; it is a heuristic, not a portable guarantee. Step 5 is what makes the
promise portable, and it is why Windows is refused (D10).

### D8 — the symlink refusal is a check, not a race-free guarantee

Confinement is `os.Root`: every name resolves against a held directory handle
(`openat2` with `RESOLVE_BENEATH` on Linux, an `lstat`-verified walk elsewhere).
A name that would leave the tree is refused **by the kernel**. This package
neither reimplements that nor second-guesses it — it is the strong guarantee,
and it is race-free.

On top of it the domain refuses one thing `os.Root` permits: a **write** whose
final component is a symbolic link. `os.Root` would follow it and keep the
result inside the root, so nothing escapes — but the bytes would land somewhere
other than the name the caller gave, which is the classic symlink plant. That is
`PathEscaped`.

It is an `Lstat` **before** the operation, so it refuses the state the package
*observed*. **A link planted between the check and the open is still followed.**
Stated plainly, in the ADR and in the package doc, because the two properties
have very different strengths and a reader who conflates them will over-trust
the weaker one.

### D9 — the operating system's cause stays in the error chain

`errors.Is(err, fs.ErrNotExist)` is the sentence every Go program that touches
files already contains. A filesystem that breaks it is a filesystem nobody can
adopt incrementally.

So `failRead` / `failWrite` / `failPublish` wrap the `*fs.PathError` as the
**cause** and restate their sentinel's reason, public, private and exit code
inline. The result answers both `errors.Is(err, vfs.ReadFailed)` and
`errors.Is(err, fs.ErrNotExist)`.

This is the one place the domain deliberately **diverges from ADR 0045**: the
session store hides the operating-system cause on purpose, because a session
error crossing a trust boundary must not describe the disk. A filesystem's
errors are about the disk. Restating drifts, so
`TestWrapHelpersRestateTheirSentinelExactly` compares each helper against its
sentinel field by field.

### D10 — Windows is refused at construction, not approximated (ADR 0018)

`NewOS` returns `proc.UnsupportedPlatform` where the mechanics do not exist.
Two of the three promises cannot be kept on Windows and the third is weaker than
it looks:

- **No directory flush.** `FlushFileBuffers` on a directory handle returns
  `ERROR_ACCESS_DENIED`. There is no API that orders the entry against the
  content, so this is not a matter of writing more code.
- **A mode is not an ACL.** Go maps an `fs.FileMode` to the read-only attribute
  and nothing else, so `0600` excludes no account. Honouring it would mean
  building a security descriptor through `CreateFileW`, which stdlib `syscall`
  does not expose — the same wall ADR 0045 hit.
- **`MoveFileEx`** with `MOVEFILE_REPLACE_EXISTING` fails outright when the
  destination is open by another process without `FILE_SHARE_DELETE`. A
  publisher whose swap fails *because a reader is reading* is not this
  primitive.

Approximating all three — calling `Chmod`, observing no error, skipping the
flush, reporting success — would produce exactly the filesystem this domain
exists not to be: one making a durability and a permission claim it cannot keep,
on the platform where nobody would think to check. The refusal is at
**construction**, so it arrives where the program is wired.

### D11 — two implementations, one set of refusals, one conformance suite

`NewMem` exists so a consumer can test its own code without a temporary
directory, and that is worth something only if the two answer identically. They
share the core guards and the same typed sentinels, and a table-driven suite
runs the **same cases** against both.

Three differences are real and are named rather than discovered: the memory
filesystem has no symbolic links, reports the **zero** `ModTime` (a stated
absence beats a plausible fiction — code that depends on modification times
depends on a real filesystem), and makes no durability claim because it has no
device.

A fourth is a performance trap and is called out in `BENCH.md`: in memory,
`WriteAtomic` (441 ns) and `WriteFile` (429 ns) are the same number within noise,
because publication is one map assignment under the write lock. **The memory
filesystem is a faithful double for the semantics of publication and tells you
nothing whatever about its cost.**

### D12 — no registry

`FullFS` is not keyed by a name. A filesystem is constructed from a root
directory or from nothing, there is no configuration string that could select
between them without also carrying that root, and a registry would exist only to
turn a compile-time choice into a runtime one. Same verdict as ADR 0026, ADR
0029 and ADR 0041, for the same reason.

## Measurements

Full tables and machine stamp in `internal/service/vfs/BENCH.md` and
`internal/core/vfs/BENCH.md`. The three that change how the domain should be
used:

**Publication costs two device round trips, and nothing else.**

| payload | `WriteAtomic` (disk) | vs 256 B |
|---|---|---|
| 256 B | 3 062 407 ns | — |
| 4 KiB | 3 069 464 ns | +0.2 % |
| 64 KiB | 3 205 589 ns | +4.7 % |

A **256× larger payload costs 4.7 % more**. The cost is the two `fsync` calls,
not the bytes. The consequence, which is the thing people get wrong: **batching
helps and buffering does not** — publishing 100 small files costs 100 × 3 ms,
because there is no payload size at which a per-call device round trip
amortises.

**Atomicity costs 9–16× an in-place write** (195 µs → 3 062 µs at 256 B; 346 µs
→ 3 206 µs at 64 KiB), and buys the 0-versus-540 torn-read result above. That is
the trade, with both halves quantified.

**The guards are free on the accepting path.** `ValidatePath` on a realistic
name is 23 ns and **0 allocations**; `ValidatePerm` on a legal mode is 2.8 ns and
**0 allocations**. Refusals allocate ~208–224 B for the typed error and its
diagnostic field, which is deliberate and on the cold path.

## Consequences / Semantics

- A caller that wants a publication asserts `AtomicWriter` or accepts `FullFS`;
  a caller that only writes scratch data uses `WriteFile` and pays nothing for a
  guarantee it does not need. **Not every write is a publication** — paying for
  a temporary plus two flushes to drop a scratch file is a cost with nothing on
  the other side.
- `PublishFailed` is safe to retry. `DirectorySyncFailed` is **not** a retry
  signal: the content is already live, and the caller decides whether
  unproven durability is acceptable for its data.
- Missing parent directories are **not** created by `WriteFile`. A typo in a
  path is far more common than a genuinely absent directory, and inventing the
  tree silently is how a file ends up somewhere nobody looks. `MkdirAll` is the
  call that says so.
- `Remove` refuses a non-empty directory (`DirectoryNotEmpty`); `RemoveAll` is
  the recursive one and is idempotent. The asymmetry is `os.Remove`'s and
  `os.RemoveAll`'s, kept deliberately.
- No `Public` string in the domain names a path. A `Public` is read by third
  parties and a path is the one piece of caller data a filesystem error always
  holds; it travels as a log-only field.

## Breaking changes

None. The domain is new; no published shape changed, so the ADR 0040 v0 licence
is not used — said out loud rather than left silent.

## Why not

**Why not `afero` / `billy`?** Both predate `io/fs` and declare their own read
interface, so adopting either would mean the SDK's filesystems are *not*
`fs.FS` and every stdlib walker needs an adapter. They also both offer
`fs.ModePerm` defaults and neither makes an atomicity claim, which is the one
thing this domain is for. The SDK's dependency budget is the secondary argument
here; the primary one is that the interface is wrong.

**Why not put `WriteAtomic` on `WritableFS`?** Because a filesystem that cannot
publish atomically is still a perfectly good filesystem, and ADR 0039 forbids
widening a published port anyway. The type assertion is the honest way to ask.

**Why not offer a cross-device fallback?** Because copy-then-delete is not
atomic, and offering it under `WriteAtomic` would mean the method's guarantee
depends on a mount table the caller cannot see. Refusing is the only answer that
keeps the name true.

**Why not sweep abandoned temporaries?** A sweeper cannot distinguish an orphan
from another process's in-flight publication without a lock, and this domain
takes no locks. The naming convention makes an orphan identifiable by a human or
by a caller's own cleanup, which is where that policy belongs.

**Why not a `Sync()` knob to skip the flushes?** The flushes *are* the
guarantee. A publication that skips them is `WriteFile` with extra steps and a
misleading name, and the domain already offers `WriteFile`.

## Deferred

- **A Windows backend** on `CreateFileW` + `SetFileSecurity` + `MoveFileEx`,
  with the absent directory flush documented rather than faked. Its own ADR.
- **`OpenFile` / streaming writes.** Every verb here takes a whole `[]byte`,
  because publication is defined on a complete file. A streaming writer is a
  different port with a different atomicity story.
- **Symlink and hardlink creation.** Deliberately absent: the memory filesystem
  would have to reimplement resolution rules the kernel owns, and the two would
  disagree the first time anyone leaned on them.
- **`chmod` / `chown` after creation.** Modes apply on creation only, which is
  what `TestModeAppliesOnCreationAndNotOnRewrite` pins.
- **Migrating `internal/service/session`'s file store onto this port.** It
  predates the domain and has its own sealed-record and `flock` requirements;
  the merge is a separate change with its own risk.

## References

- `internal/core/vfs/CLAUDE.md`, `internal/service/vfs/CLAUDE.md`,
  `pkg/v1/vfs/CLAUDE.md`
- `internal/core/vfs/BENCH.md`, `internal/service/vfs/BENCH.md`
- `internal/service/vfs/publish_internal_test.go` — the failure-injection proof
- `internal/service/vfs/torn_external_test.go` — the in-place-versus-published
  measurement
