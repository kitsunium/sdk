# internal/service/data/vfs/

The reference half of this file moved, unchanged and under the same section titles, to
`internal/service/data/vfs/NOTES.md` (ADR 0253): read the section a change touches.
It holds:
§Contents; §Atomic publication — the five steps and why each one is there.

## Purpose

Implements the filesystem port declared in `internal/core/data/vfs` (**ADR 0056**):
two concrete filesystems — `NewOS` (confined to one directory tree) and
`NewMem` (held in a map) — plus the atomic publication both of them promise.

Code range: `0.3.55.*` (ADR 0056) — the two outcomes a *concrete* filesystem
can produce and an abstract one cannot, `RootUnavailable` and
`DirectorySyncFailed`. Since ADR 0160 they are declared with the port's verdicts
in `internal/core/data/vfs`, under the values this package allocated; this
package declares no code and restates them in its wrap helpers.

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
- **Do NOT rename a batch's file before every flush of its window has
  returned, nor past a file that failed.** The prefix semantics are the
  port's: the count names the files renamed from the first, and a rename
  after a failed file would publish one the count does not name.
  `TestABatchWhoseKthFlushFailsPublishesTheFirstKAndNothingAfter` holds it.
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
