# internal/service/data/vfs/

## Purpose

Implements the filesystem port declared in `internal/core/data/vfs` (**ADR 0056**):
two concrete filesystems — `NewOS` (confined to one directory tree) and
`NewMem` (held in a map) — plus the atomic publication both of them promise.

Code range: `0.3.55.*` (ADR 0056) — the two outcomes a *concrete* filesystem
can produce and an abstract one cannot, `RootUnavailable` and
`DirectorySyncFailed`. Since ADR 0160 they are declared with the port's verdicts
in `internal/core/data/vfs`, under the values this package allocated; this
package declares no code and restates them in its wrap helpers.

## Contents

| File | What lives there |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `vfs.go` | `verdict`, and the `failRead` / `failWrite` / `failPublish` wrap helpers |
| `os.go` | `NewOS`, the read half (delegating to `os.Root`) — `Stat`, `ReadDir`, `ReadFile`, and the link shortcuts `Lstat` / `ReadLink` forwarded to `os.Root`, which the plain `fs.FS` it held used to hide (#1053) —, the write guards, and `Close` — `io.Closer` by type assertion (ADR 0039), releasing the root's descriptor rather than waiting for `os.Root`'s finalizer |
| `os_write.go` | `WriteFile`, `MkdirAll`, `Remove`, `RemoveAll` — the non-atomic verbs |
| `os_publish.go` | **the domain's reason to exist**: `publish` (`stage`, steps 1 to 4, then step 5), `atomicOps`, `createTemp` — the temporary created exclusively, `tempPath`'s name first, then `randomTempPath`'s while the name tried is taken, `tempAttempts` names at most (#813); `WriteAtomicAll` — `core/data/vfs.AtomicBatchWriter`: `stage` per file, then `syncDirs` once per directory (`publishAll`, #827); and `SyncDir` — `core/data/vfs.DirSyncer`, step 5 offered for a directory just created (#1060) — with `failDirSync`, the `DirectorySyncFailed` restatement both share |
| `os_dirflush.go` | `dirFlushes`: step 5 of the disk engine's publications through one `kernel/concur/commit` group per directory, so the publications into one directory that reach it while a flush runs share the next one (#803); a directory's group lives while a publication waits on it, and the entry dropped is kept as the next directory's (`spare`, `bind`), so an uncontended publication builds no group and flushes on its own goroutine through the group's idle path (#813) |
| `osguard_unix.go` / `osguard_other.go` | `platformNative` + `syncDirHandle`, and the honest refusal where the mechanics do not exist |
| `mem.go` | `NewMem`, the read half — `Lstat` is `Stat` and `ReadLink` `READ_FAILED` over `fs.ErrInvalid`, since no link exists here —, `SyncDir` (the disk engine's refusals, nothing to flush), the locked helpers |
| `mem_write.go` | the memory write verbs, including `WriteAtomic`, which applies `perm` on every publication as the disk engine's rename does, where `WriteFile` applies it on creation only (#1052) |
| `mem_node.go` / `mem_info.go` / `mem_file.go` / `mem_dir.go` | the tree entry, its `fs.FileInfo`, and the two open handles |
| `BENCH.md` | what publication costs, and what it is that costs |
| `alloc_external_test.go` + `allocwindow_test.go` | race off, the alloc lane's: an uncontended `WriteAtomic` on the disk engine allocates at most 22 times on the caller's stack — 19 on linux/amd64, 22 on CI's linux/386 runner (#813); `allocwindow_test.go` is the SDK's one allocation window, copied byte for byte |
| `tempname_internal_test.go` | the temporary's name: unique, beside its target, one allocation; a taken name never opened, the random fallback and its bound; a planted next name neither followed nor overwritten; a destination named like the next temporary never created as its own temporary (#813) |

## Atomic publication — the five steps and why each one is there

`WriteAtomic` is the call the domain exists for. The order **is** the contract:

1. **create a temporary in the SAME directory as the target.** Same directory
   means same filesystem by construction, so the rename in step 4 cannot fail
   with `EXDEV` — a cross-device rename is not *handled* here because it is not
   *reachable* here. `TestTheTemporaryLivesBesideItsTarget` pins it, because the
   day somebody "tidies" this into `os.TempDir` the result is a cross-device
   rename in production and nothing at all in the tests.
   The name is `.vfs-<seed><counter>.tmp`: eight random bytes drawn once per
   process, then a process-wide counter, sixteen hex digits each, built on the
   stack — one allocation, no entropy call (#813). It is created with
   `O_EXCL` through the root, so a name that exists — a file, a link — is
   refused, never opened. Another process that can read the directory can
   guess the next name and plant it; a taken name therefore falls back to
   sixteen random bytes, which nobody can plant ahead, and `tempAttempts` (3)
   names taken end the publication with `PublishFailed` over `fs.ErrExist`.
   A candidate equal to the destination — compared without case, the same
   entry on a case-insensitive filesystem (darwin's APFS) — is drawn again
   (`tempCandidate`): a
   destination may be named like a temporary, and an absent one would
   otherwise be created as its own temporary and written in place, visible
   half-written.
2. **write the payload.**
3. **flush the FILE.** A rename over data still sitting in the page cache is
   atomic about nothing once the machine loses power.
4. **rename.** The indivisible step. POSIX requires `rename(2)` to replace
   atomically, so a concurrent reader gets the old inode or the new one.
5. **flush the DIRECTORY.** Without it the entry naming the new inode may not
   survive a crash, and the file comes back with content and no name.
   On the disk engine, the publications into one directory that reach this
   step while a flush of it runs share the NEXT flush (`os_dirflush.go`, #803):
   each joins the batch still gathering — never the one already flushing — so
   the flush it waits for began after its own rename, and every participant
   gets that flush's verdict. A publication that finds its directory idle
   flushes it on its own goroutine (the commit group's idle path, #813): the
   same fsync, begun after its rename and returned before its verdict, with
   no batch built; the publications arriving meanwhile gather into the batch
   that flushes after it.

**Steps 1–4 failing mean nothing was published.** The temporary is removed and
the previous bytes are untouched — that is what returning `PublishFailed`
*asserts*, and `publish_internal_test.go` proves it by hashing the destination
before and after a failure injected at each step, using a **real** file handle
so the temporary is genuinely half-written on the write case.

**Step 5 failing means the opposite** and gets its own code,
`DirectorySyncFailed`. The rename already happened, every reader now sees the
new content, and the only thing in doubt is durability. It is deliberately
**not** rolled back: undoing it would be a second, non-atomic write to repair a
durability problem, which is how a good file gets replaced by a worse one.

### A publication's mode, and the directory above it

- **`perm` applies on every publication.** A publication is a new inode
  renamed over the old one, so the file afterwards has the mode it was
  published with, whatever the replaced file had; the memory engine stores
  `perm` on every `WriteAtomic` to answer identically. `WriteFile` rewrites in
  place and keeps os.WriteFile's creation-only rule.
  `TestAPublicationAppliesItsModeOnEveryEngine` and
  `TestModeAppliesOnCreationAndNotOnRewrite` pin the two (ADR 0056, amended
  2026-11-02).
- **Step 5 flushes the directory naming the file, and nothing above it.** A
  directory `MkdirAll` created needs its parent flushed for its own entry to
  survive a power loss: `SyncDir` — the `DirSyncer` sibling, reached by type
  assertion and not embedded in `FullFS` — opens the directory through the
  root and runs the same mechanic. `NOT_REGULAR_FILE` for a file,
  `DIRECTORY_SYNC_FAILED` with the cause kept for a directory it cannot open
  or flush. docstore's `Open` calls it on the parent of every directory it
  creates. `TestSyncDirAnswersIdenticallyOnEveryEngine`.
- **A batch shares step 5, and keeps steps 1 to 4 per file (#827).**
  `WriteAtomicAll` — the `AtomicBatchWriter` sibling, not in `FullFS` —
  checks every target first (a refusal publishes nothing), then runs each
  file's temporary, write, file flush and rename, then flushes each directory
  once, after its last rename: a flush makes every earlier rename into the
  directory durable, so a batch of sixteen pays sixteen file flushes and one
  directory flush where `WriteAtomic` pays two flushes a file. A failure at a
  file's steps 1 to 4 stops the batch: the count returned names the files
  renamed, whose directories are flushed before the call returns, and the rest
  keep their previous bytes; a directory flush that fails is
  `DIRECTORY_SYNC_FAILED`, winning over `PUBLISH_FAILED`, never rolled back.
  The memory engine checks every target under the lock and assigns them all.
  `TestWriteAtomicAllAnswersIdenticallyOnEveryEngine`,
  `TestABatchFlushesEachFileOnceAndItsDirectoryOnce` (the flush counts, by the
  `atomicOps` double), `TestABatchThatFailsAtTheKthFileLeavesTheFirstKDurableAndTheRestUntouched`.

### What it does not guarantee

- **`rename(2)` is atomic within one filesystem, and that is all.** Step 1
  makes the cross-device case unreachable rather than solving it.
- **Durability is `fsync`'s, and `fsync` can lie.** A device that acknowledges a
  flush it has only buffered will lose a published file on power loss, and no
  code here can detect that.
- **There is no locking, and none is wanted.** Two processes publishing the same
  name concurrently both succeed; the winner is whoever renamed last, and no
  reader ever sees a mixture. Adding a lock would buy ordering nobody asked for
  and cost the property that makes this usable across processes at all. (The
  `flock(2)` lesson `internal/service/security/session` learned does **not** apply here,
  because this package takes no `flock`.)
- **The symlink refusal is a check, not a race-free guarantee.** See below.

## What `NewOS` confines, and what it only observes

Confinement is `os.Root`: every name resolves against a held directory handle
(`openat2` with `RESOLVE_BENEATH` on Linux, an `lstat`-verified walk elsewhere).
A name that would leave the tree is refused **by the kernel**. That is the
strong guarantee, and this package neither reimplements it nor second-guesses it.

On top of it, this package refuses one thing `os.Root` permits: a **write** whose
final component is a symbolic link. `os.Root` would follow it and keep the result
inside the root, so nothing escapes — but the bytes would land somewhere other
than the name the caller gave, which is the classic symlink plant. That refusal
is `PathEscaped`.

It is an `Lstat` **before** the operation, so it judges the state this package
*observed*. A link planted between the check and the open is still followed.
Stated plainly because the distinction matters: the race-free property is
confinement to the root, and that one is the kernel's.

## The two filesystems, and where they differ on purpose

The memory filesystem is only worth having if it refuses what the real one
refuses, so both run the same core guards and a table-driven conformance suite
runs the **same cases** against both. Three differences are real and are named
rather than discovered:

| | `NewOS` | `NewMem` |
|---|---|---|
| symbolic links | resolved and refused as writes | none exist |
| `ModTime` | the operating system's | the **zero** time, deliberately |
| durability | `fsync` on file and directory | none — there is no device |
| cost of `WriteAtomic` | ~3 ms (two flushes), 20 allocations — `os.Root` 18 of them (#813) | ~441 ns (one map assignment) |

That last row is why the memory filesystem is a faithful double for the
*semantics* of publication and tells you **nothing** about its cost. See
`BENCH.md`.

## Where it refuses to run at all

`NewOS` returns `proc.UnsupportedPlatform` at **construction** (ADR 0018) on a
GOOS lacking the mechanics its guarantees rest on. Windows lacks two of three:
there is no way to flush a directory (`FlushFileBuffers` on a directory handle
returns `ERROR_ACCESS_DENIED`), and an `fs.FileMode` is not an ACL, so `0600`
excludes nobody. `osguard_other.go` carries the full argument. `NewMem` works
everywhere.

The suite holds the refusal to the same standard as the filesystem. Every case
that needs a disk filesystem — the conformance table's `disk` row, the sabotaged
publications in `publish_internal_test.go`, `TestTheOSFilesystemClosesItsRoot` —
first ASSERTS that `NewOS` refused with `UnsupportedPlatform` and handed back
nothing, and only then skips, naming why (`refusedByDesign`, `newDiskFS`). A
platform that refused with anything else, or not at all, fails; the Windows
lane runs all of it (ADR 0095).

## Conventions

- **The operating system's cause stays in the chain.** `failRead` / `failWrite`
  / `failPublish` *restate* their sentinel's reason, public, private and exit
  code inline so `errs` can keep the `*fs.PathError` as the cause. That is what
  keeps `errors.Is(err, fs.ErrNotExist)` — the sentence every Go program that
  touches files already contains — answering. This is the one place the domain
  deliberately diverges from `internal/service/security/session`, which hides its cause.
  `TestWrapHelpersRestateTheirSentinelExactly` stops the restatement drifting.
- **`atomicOps` is a struct of functions so `publish` is a pure function of its
  dependencies.** That is the only reason the failure paths are reachable at
  all: a write that stops halfway or a `Sync` that errors cannot be provoked on
  a real file without a hostile filesystem.
- **Both engines offer the same stdlib shortcuts**: `fs.StatFS`,
  `fs.ReadDirFS`, `fs.ReadFileFS` and `fs.ReadLinkFS`
  (`TestBothFilesystemsImplementTheStdlibShortcuts`), and both pass
  `testing/fstest.TestFS` (`TestEveryEngineIsAValidIoFS`) — the standard
  library's own conformance test for an `fs.FS`.
- **One store-wide `RWMutex` for `memFS`.** The map is a filesystem *tree*, and
  `MkdirAll` / `RemoveAll` / `WriteFile` are multi-key transactions. See the
  `.ktn-linter.yaml` entry for why `sync.Map` cannot express them.

## Do NOT

- **Do NOT move the temporary out of the target's directory.** It is the
  same-filesystem guarantee; `os.TempDir` would make `EXDEV` reachable in
  production and invisible in tests.
- **Do NOT roll back on a directory-flush failure.** The content is already
  published; a second non-atomic write is strictly worse than a truthful error.
- **Do NOT let a publication take a directory flush already running as its
  own.** It may have begun before the publication's rename, and then flushes
  nothing the publication needs. `dirFlushes` joins the batch still gathering;
  `TestEveryPublicationsFlushBeginsAfterItsRename` fails on the variant that
  waits for the running flush instead.
- **Do NOT let the cleanup failure replace the cause.** `discard` reports "the
  disk is full", not "the orphan would not unlink" — the consequence must not
  displace the problem.
- **Do NOT hide the operating system's error.** Wrap it, never replace it.
- **Do NOT add an `fsync`-less "fast" publication mode.** The flushes *are* the
  guarantee; a publication that skips them is `WriteFile` with extra steps.
- **Do NOT approximate the domain on Windows.** ADR 0018: an honest refusal
  beats an untested implementation of a durability boundary.

## Verification

```bash
cd internal/service && GOWORK=off go test -race ./data/vfs/...
# coverage today: 85.1% (the remainder is device-failure branches that need a
# hostile filesystem, plus the _other.go stubs this GOOS does not compile)
cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem ./data/vfs/
```
