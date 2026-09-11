# internal/service/vfs/

## Purpose

Implements the filesystem port declared in `internal/core/vfs` (**ADR 0056**):
two concrete filesystems — `NewOS` (confined to one directory tree) and
`NewMem` (held in a map) — plus the atomic publication both of them promise.

Code range: `0.3.55.*` (ADR 0056). Only the two outcomes a *concrete*
filesystem can produce and an abstract one cannot are declared here;
the port's verdicts stay in `internal/core/vfs`.

## Contents

| File | What lives there |
|---|---|
| `vfs.go` | package doc, `verdict`, and the `failRead` / `failWrite` / `failPublish` wrap helpers |
| `os.go` | `NewOS`, the read half (delegating to `os.Root`), and the write guards |
| `os_write.go` | `WriteFile`, `MkdirAll`, `Remove`, `RemoveAll` — the non-atomic verbs |
| `os_publish.go` | **the domain's reason to exist**: `publish`, `atomicOps`, `tempPath` |
| `osguard_unix.go` / `osguard_other.go` | `platformNative` + `syncDirHandle`, and the honest refusal where the mechanics do not exist |
| `mem.go` | `NewMem`, the read half, the locked helpers |
| `mem_write.go` | the memory write verbs, including `WriteAtomic` |
| `mem_node.go` / `mem_info.go` / `mem_file.go` / `mem_dir.go` | the tree entry, its `fs.FileInfo`, and the two open handles |
| `codes.go` / `errors.go` | `RootUnavailable` (`0.3.55.1`), `DirectorySyncFailed` (`0.3.55.2`) |
| `BENCH.md` | what publication costs, and what it is that costs |

## Atomic publication — the five steps and why each one is there

`WriteAtomic` is the call the domain exists for. The order **is** the contract:

1. **create a temporary in the SAME directory as the target.** Same directory
   means same filesystem by construction, so the rename in step 4 cannot fail
   with `EXDEV` — a cross-device rename is not *handled* here because it is not
   *reachable* here. `TestTheTemporaryLivesBesideItsTarget` pins it, because the
   day somebody "tidies" this into `os.TempDir` the result is a cross-device
   rename in production and nothing at all in the tests.
2. **write the payload.**
3. **flush the FILE.** A rename over data still sitting in the page cache is
   atomic about nothing once the machine loses power.
4. **rename.** The indivisible step. POSIX requires `rename(2)` to replace
   atomically, so a concurrent reader gets the old inode or the new one.
5. **flush the DIRECTORY.** Without it the entry naming the new inode may not
   survive a crash, and the file comes back with content and no name.

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
  `flock(2)` lesson `internal/service/session` learned does **not** apply here,
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
| cost of `WriteAtomic` | ~3 ms (two flushes) | ~441 ns (one map assignment) |

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

## Conventions

- **The operating system's cause stays in the chain.** `failRead` / `failWrite`
  / `failPublish` *restate* their sentinel's reason, public, private and exit
  code inline so `errs` can keep the `*fs.PathError` as the cause. That is what
  keeps `errors.Is(err, fs.ErrNotExist)` — the sentence every Go program that
  touches files already contains — answering. This is the one place the domain
  deliberately diverges from `internal/service/session`, which hides its cause.
  `TestWrapHelpersRestateTheirSentinelExactly` stops the restatement drifting.
- **`atomicOps` is a struct of functions so `publish` is a pure function of its
  dependencies.** That is the only reason the failure paths are reachable at
  all: a write that stops halfway or a `Sync` that errors cannot be provoked on
  a real file without a hostile filesystem.
- **One store-wide `RWMutex` for `memFS`.** The map is a filesystem *tree*, and
  `MkdirAll` / `RemoveAll` / `WriteFile` are multi-key transactions. See the
  `.ktn-linter.yaml` entry for why `sync.Map` cannot express them.

## Do NOT

- **Do NOT move the temporary out of the target's directory.** It is the
  same-filesystem guarantee; `os.TempDir` would make `EXDEV` reachable in
  production and invisible in tests.
- **Do NOT roll back on a directory-flush failure.** The content is already
  published; a second non-atomic write is strictly worse than a truthful error.
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
cd internal/service && GOWORK=off go test -race ./vfs/...
# coverage today: 85.1% (the remainder is device-failure branches that need a
# hostile filesystem, plus the _other.go stubs this GOOS does not compile)
cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem ./vfs/
```
