# internal/service/data/vfs — notes

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles.

## Contents

| File | What lives there |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `vfs.go` | `verdict`, and the `failRead` / `failWrite` / `failPublish` wrap helpers |
| `os.go` | `NewOS`, the read half (delegating to `os.Root`) — `Stat`, `ReadDir`, `ReadFile`, and the link shortcuts `Lstat` / `ReadLink` forwarded to `os.Root`, which the plain `fs.FS` it held used to hide (#1053) —, the write guards, and `Close` — `io.Closer` by type assertion (ADR 0039), releasing the root's descriptor rather than waiting for `os.Root`'s finalizer |
| `os_write.go` | `WriteFile`, `MkdirAll`, `Remove`, `RemoveAll` — the non-atomic verbs |
| `os_publish.go` | **the domain's reason to exist**: `publish` (`stage`, steps 1 to 4, then step 5), `atomicOps`, `createTemp` — the temporary created exclusively, `tempPath`'s name first, then `randomTempPath`'s while the name tried is taken, `tempAttempts` names at most (#813); `WriteAtomicAll` — `core/data/vfs.AtomicBatchWriter`: windows of `publishWindow` (16) files, each created and written in order with its flush started at once on a goroutine of its own (`writeWindow`, `windowFlusher` over `kernel/concur/group`), then the renames in order up to the first failure (`stageWindow`, `discardAll`), then `syncDirs` once per directory (`publishAll`, #827); and `SyncDir` — `core/data/vfs.DirSyncer`, step 5 offered for a directory just created (#1060) — with `failDirSync`, the `DirectorySyncFailed` restatement both share |
| `os_dirflush.go` | `dirFlushes`: step 5 of the disk engine's publications through one `kernel/concur/commit` group per directory, so the publications into one directory that reach it while a flush runs share the next one (#803); a directory's group lives while a publication waits on it, and the entry dropped is kept as the next directory's (`spare`, `bind`), so an uncontended publication builds no group and flushes on its own goroutine through the group's idle path (#813) |
| `osguard_unix.go` / `osguard_other.go` | `platformNative` + `syncDirHandle`, and the honest refusal where the mechanics do not exist |
| `mem.go` | `NewMem`, the read half — `Lstat` is `Stat` and `ReadLink` `READ_FAILED` over `fs.ErrInvalid`, since no link exists here —, `SyncDir` (the disk engine's refusals, nothing to flush), the locked helpers |
| `mem_write.go` | the memory write verbs, including `WriteAtomic`, which applies `perm` on every publication as the disk engine's rename does, where `WriteFile` applies it on creation only (#1052) |
| `mem_node.go` / `mem_info.go` / `mem_file.go` / `mem_dir.go` | the tree entry, its `fs.FileInfo`, and the two open handles |
| `BENCH.md` | what publication costs, and what it is that costs |
| `alloc_external_test.go` + `allocwindow_test.go` | race off, the alloc lane's: an uncontended `WriteAtomic` on the disk engine allocates at most 22 times on the caller's stack — 19 on linux/amd64, 22 on CI's linux/386 runner (#813); `allocwindow_test.go` is the SDK's one allocation window, copied byte for byte |
| `publish_window_internal_test.go` | a batch's windows (#827, ADR 0056 amended 2026-11-15): a window's flushes in flight together and never more than a window's temporaries open, the renames in the batch's order; a flush failing at file k, the later ones of its window succeeding, publishes files 0 to k−1 and nothing after, no temporary surviving — over a probe whose flushes wait at a barrier for their window |
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
  directory flush where `WriteAtomic` pays two flushes a file. **The file
  flushes of a window of sixteen are in flight together** (ADR 0056, amended
  2026-11-15): the temporaries are created and written in order, each one's
  `Sync` and `Close` started on a goroutine as soon as it is written, and the
  renames run in order once every flush of the window has returned — a
  journal commits concurrent flushes together, so sixteen cost a few device
  round trips. A failure at a file's steps 1 to 4 — the lowest index, whatever
  order the flushes answered in — stops the batch: that file's temporary and
  every later one are removed, the count returned names the files renamed,
  whose directories are flushed before the call returns, and the rest keep
  their previous bytes; a directory flush that fails is
  `DIRECTORY_SYNC_FAILED`, winning over `PUBLISH_FAILED`, never rolled back.
  The memory engine checks every target under the lock and assigns them all.
  `TestWriteAtomicAllAnswersIdenticallyOnEveryEngine`,
  `TestABatchFlushesEachFileOnceAndItsDirectoryOnce` (the flush counts, by the
  `atomicOps` double, its file count atomic since the flushes run
  together), `TestABatchThatFailsAtTheKthFileLeavesTheFirstKDurableAndTheRestUntouched`,
  `TestABatchFlushesAWindowAtOnceAndRenamesInOrder`,
  `TestABatchWhoseKthFlushFailsPublishesTheFirstKAndNothingAfter`.

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
- **A crash leaves at most one window of orphans.** A process killed while a
  batch's window is in flight leaves up to sixteen `.vfs-*.tmp` temporaries
  (one for `WriteAtomic`), never a partial file at a published name; they
  are the same orphans as before, which nothing lists as content.
- **The symlink refusal is a check, not a race-free guarantee.** See below.
