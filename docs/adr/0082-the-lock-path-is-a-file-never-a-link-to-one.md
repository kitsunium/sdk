# ADR 0082 — the lock path is a file, never a link to one

- **Status**: Accepted
- **Date**: 2026-09-13
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0052](0052-sdk-lock-domain.md) §D6 (the lock directory's permission rule was the whole of the file locker's answer to substitution; it is not), [ADR 0081](0081-the-windows-file-lock-is-a-different-primitive.md) §Deferred (the reparse-point item, deferred there on an explicit "both sides or not at all", is closed here on both sides)
- **Related**: [ADR 0018](0018-sdk-cross-platform-portability.md) (the build-tag split, the runtime bar, the `x/sys` ban), [ADR 0056](0056-sdk-vfs-domain.md) (`PathEscaped`: a symlink at a path the SDK owns already has a verdict of its own), [ADR 0054](0054-sdk-queue-domain.md) (`QueueDirectoryUnusable` with `why=symlink`, the same refusal one domain over), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) (the code allocation)

## Context

The file locker derives its filename from the lock name:
`hex(sha256(name)) + ".lock"`, inside `FileConfig.Dir`. That mapping was chosen
so no caller-supplied string reaches the filesystem verbatim — no traversal, no
length limit, no case-folding collision. It succeeds at that, and it has a
property nobody wrote down: the name is **derived, therefore predictable**. An
attacker who knows the lock directory and the lock name can compute the
filename exactly, and predictable is all the attack needs.

`checkDir` exists to refuse a directory in which lock files can be substituted.
Its rule is "refuse world-writable-and-not-sticky", and ADR 0081 §D5 pins the
whole table including the accepting rows. It does not reach this attack, for a
reason that is structural rather than an oversight: **the sticky bit governs
UNLINKING an entry that exists, and this attack CREATES one at a name nobody
has taken yet.** `0777|sticky` — exactly what `/tmp` is, and a row the table
explicitly accepts — passes.

Probed on linux/amd64 against `pkg/v1/lock` as it shipped, from an external
program: create the lock directory `0777|os.ModeSticky`, acquire and release
once to learn the filename, remove it, plant `os.Symlink(elsewhere, thatName)`,
acquire again.

```
répertoire 0777|sticky : ACCEPTÉ
acquisition sur lien planté : held=true err=<nil>
SUIVI : le verrou a atterri sur /tmp/sym.../elsewhere.lock
        contenu du registre redirigé = "1\n"
```

The acquisition **succeeded**. The `flock` and the fencing ledger landed on a
file outside the checked directory, and no layer reported anything.

What that buys the planter, measured rather than asserted:

- **Two processes inside one section.** The victim's lock covers the planter's
  target inode; the planter's own lock covers the real filename. Both hold a
  lease, neither is blocked, nothing logs. This is the failure the whole domain
  exists to prevent, reached with no race at all.
- **The fencing token, chosen by the planter.** The redirected ledger is the
  planter's file, so `readFence` reads *their* number. A symlink to a file
  containing `48213\n` — a pidfile is exactly that shape — made `Acquire`
  return fence **48214** and rewrite the pidfile with it.
- **Not arbitrary truncation**, and the difference was measured rather than
  assumed. A symlink to a file whose content is not a decimal counter is
  refused by `readFence` with `LOCK_FENCE_CORRUPT` **before** `writeFence`
  truncates: `"DES DONNÉES QUI COMPTENT\n"` survived the attempt byte for byte.
  The clobber reaches empty files and decimal ones, which is narrower than
  "any file the victim can write" and worth stating as the narrower thing.

A real consumer is exposed. `kodflow/ktn-linter`'s MCP daemon puts its instance
lock in `/tmp/ktn-linter-$UID/ktn-linter/` when `XDG_RUNTIME_DIR` is unset, and
a planter who pre-creates that path `0777|sticky` clears `checkDir` today.

ADR 0081 §Deferred saw the Windows half of this and deferred it, on an argument
that was correct at the time and is now spent:

> It is deliberately not refused here … **the shipped Unix backend does exactly
> the same thing, in a directory shape the POSIX rule explicitly accepts.** …
> So this is not a Windows gap, it is a `lock` gap with the same reach on both
> kernels, and closing it on one platform only would make the same deployment
> succeed on Linux and fail on Windows for a reason the error could not
> explain. It closes with an `O_NOFOLLOW`-equivalent open on **both** sides or
> not at all, and that is a change to the domain's contract, not to this
> backend.

The Unix half is now confirmed live by the probe above, so the symmetric answer
is to close **both**, not neither. This ADR is that close. It is its own record
rather than an amendment to 0081's Decision for the reason 0081's own sentence
gives: it changes the **domain's** contract on every platform, adds an error
code, and turns a previously-accepted deployment into a refusal — none of which
belongs inside a record about one backend.

## Decision

### D1 — The lock file is opened without following an indirection at its name

`takeFlock` no longer calls `os.OpenFile` directly. It calls `openLockFile`,
one function per platform behind the build-tag split `flock_*.go` already uses:

| Build tag | Mechanism |
|---|---|
| `linux \|\| darwin \|\| freebsd \|\| openbsd \|\| netbsd \|\| dragonfly` | `O_NOFOLLOW` on the open; the **kernel refuses** |
| `windows` | `FILE_FLAG_OPEN_REPARSE_POINT` on the open; the open **succeeds on the link** and the handle is then rejected |
| everything else | a plain open — unreachable, `platformNative` refuses first |

The tag sets are *identical* to `flock_*.go`'s, not merely similar. Solaris has
`O_NOFOLLOW` and is nevertheless served by the `other` file, because a platform
gaining one half of a pair is how a platform ends up with a lock and no
hardening, or hardening and no lock. A platform gains both files or neither.

### D2 — Windows: the flag OPENS the link, so the check is the pair

`FILE_FLAG_OPEN_REPARSE_POINT` means "give me a handle to the reparse point
itself", not "fail if there is one". Alone it fixes the redirection — the range
lock and the ledger stop landing on the planter's file — and leaves the lock
sitting on a link the planter still owns and can retarget. So the flag is
paired with `GetFileInformationByHandle`: a handle whose attributes carry
`FILE_ATTRIBUTE_REPARSE_POINT` is closed and refused.

The flag makes the check possible (without it the handle is the *target*, which
has no reparse attribute and nothing to notice); the check turns a redirect
into a refusal. Neither half is sufficient.

The open stays `os.OpenFile`. `go1.27`'s `syscall.Open` forwards the high 12
bits of its flag word to `CreateFileW`'s `dwFlagsAndAttributes` and
`FILE_FLAG_OPEN_REPARSE_POINT` is in its `validFileFlagsMask`, so no
hand-rolled `CreateFile` is needed and none is written. That is not tidiness:
ADR 0081 §D5 accepts every directory on Windows on the strength of one
sentence — "`os.OpenFile` reaches `CreateFileW` **without** `FILE_SHARE_DELETE`,
so a held lock file can be neither deleted nor renamed whatever the ACL says" —
and a hand-rolled share mode that drifts is how that sentence quietly stops
being true. The sentence stays literally true.

### D3 — `LOCK_PATH_REDIRECTED` (`0.3.51.4`), not `LOCK_BACKEND_FAILED`

A new sentinel in `internal/service/lock`, aliased at `pkg/v1/lock`, carrying
`EX_CONFIG` (78) — the same exit code as `LOCK_DIRECTORY_UNSAFE`, because it is
the same remedy: a human looks at the directory, and no retry helps.

`LOCK_BACKEND_FAILED` was rejected on the strongest ground available: it
describes a medium that could not answer, and it invites a **retry**. Nothing
failed here — a deliberate substitution succeeded — and a retry is the one
response that is wrong. `LOCK_DIRECTORY_UNSAFE` was rejected too: the directory
may be perfectly well-permissioned, and reusing it would send an operator to
`chmod` a directory that is already correct.

The SDK already answers this question the same way twice: `vfs` gives a symlink
`PathEscaped` rather than its generic verdict ("a write here would land on the
link's target"), and `queue` refuses a symlinked state directory with
`QueueDirectoryUnusable` and `why=symlink`. A third domain meeting a planted
link and calling it a backend fault would be the odd one out.

### D4 — The refusal does not branch on the errno, and the fields still carry it

`O_NOFOLLOW` on a symlink is `ELOOP` on Linux, **measured**: errno 40, on
kernel 6.12, for a live link, a dangling link and a link to a directory alike.
It is documented as `EMLINK` on FreeBSD and DragonFly, `EFTYPE` on NetBSD, and
`ELOOP` on OpenBSD and Darwin. Three values across six kernels is the shape of
table that is wrong on the seventh, so the decision consults none of them: on a
failed open, `os.Lstat` answers the question the errnos were only evidence for,
identically everywhere. The errno travels as the `observed` field so an
operator sees what the kernel actually said.

`Lstat` is **diagnosis, never the security decision.** The refusal was already
taken by the kernel one line earlier. A planter who removes the link between
the open and the `Lstat` changes which sentinel is reported and cannot change
whether the open was refused — which is why this is not the TOCTOU the check-
then-open shape would have been.

### D5 — The test is the probe, and it runs on six kernels

`nofollow_unix_test.go` is the probe reduced to a table, under the same build
tag as the code: plant nothing / a symlink to a missing file / a symlink to a
decimal file / a symlink to a directory / a hard link, in a `0777|sticky`
directory, with the filename learned the way the planter learns it. The
accepting rows are there for the reason ADR 0081 §D5's table has its own — a
guard that refuses the obvious case while quietly refusing a legitimate one is
a different bug wearing the same green tick.

Each refusing row also asserts the redirect target was **left alone**, because
a refusal that still wrote through the link is a refusal in name only.

The `e2e-cross` lane already carries `./lock` in `SERVICE_PKGS`, so this table
executes on linux, darwin, freebsd, openbsd and netbsd. That is what turns "the
errno differs per kernel" from a comment into five real refusals.

### D6 — The Windows test may not be able to run, and it says so out loud

Creating a **symbolic link** on Windows needs `SeCreateSymbolicLinkPrivilege`,
which an unprivileged account does not hold unless Developer Mode is on.
Whether GitHub's `windows-latest` image grants it is not something this
repository controls, and a test that skips on a runner nobody watches is
indistinguishable from a test that was never written.

So the Windows table plants **two** indirections and they exercise the two
halves separately:

- a **symbolic link** — a file reparse point: the open succeeds and
  `refuseReparseHandle` refuses it. Needs the privilege.
- a **junction** — a directory reparse point: the open fails outright
  (read-write on a directory needs `FILE_FLAG_BACKUP_SEMANTICS`) and
  `classifyOpenFailure` names it. Created with `mklink /J`, which needs **no
  privilege at all**.

A row that cannot be planted reports why through `t.Skip`; the parent test
**fails** if both are lost, naming this ADR's item as unproven on that lane.

What a green lane proves is therefore "at least one of the two reached a
kernel", and **not which one** — and that limit is measured rather than
assumed: `e2e-cross` runs `go test` without `-v`, and `go test` buffers a
passing package's output and discards it, for `t.Log` and for a raw
`fmt.Println` alike. There is no spelling of the count that a non-verbose lane
prints. Reading which row ran means running the file with `-v`.

Measured on this change's first run: the `windows` job of `e2e-cross` reported
`ok github.com/kitsunium/sdk/internal/service/lock 2.735s` on `windows-latest`,
so the table ran and at least one indirection was planted and refused. Whether
that included the symbolic-link row is exactly what the paragraph above says
cannot be read from there.

## Consequences / Semantics

- **A deployment that worked now fails, by design.** Any caller whose lock path
  is a symbolic link — including a deliberate one, e.g. a lock directory entry
  symlinked onto a tmpfs — is refused with `LOCK_PATH_REDIRECTED`. This is the
  point of the change and it is stated in §Breaking changes rather than buried.
  `FileConfig.Dir` itself may still be a symlink; only the final component is
  governed.
- **`O_NOFOLLOW` governs the FINAL component only**, measured: a symlink at a
  *parent* component is still traversed. That is a different and much weaker
  exposure — the parents are `FileConfig.Dir`, which the caller chose, while
  the final component is a name this package derives and a planter can predict.
  Named here rather than implied to be covered; see §Deferred.
- **A hard link at the lock path is still accepted**, on both kernels. It is
  not an indirection: the name resolves to a real inode and `O_NOFOLLOW` says
  nothing about it. It is also a much weaker primitive for a planter, who must
  already hold the target they would be redirecting to.
- **A FIFO, device or socket at the lock path was already refused, loudly.**
  Measured: `mkfifo` at the lock path yields `LOCK_BACKEND_FAILED` from the
  ledger's positional read, not a silent split-brain. So no general
  "regular file only" rule was added; widening the change would have been
  scope, not coverage.
- **The ordinary path is unchanged and pinned as such.** A normal acquisition,
  a contended one, and release-then-re-acquire over an existing regular lock
  file are asserted in their own test, separate from the table, so a regression
  there reads as "locking stopped working" rather than "the hardening is
  missing".
- **The dependency graph is untouched**: no module added to any `go.mod`, no
  `go.sum` entry, no `MODULE.bazel` change. `pkg` consumers inherit nothing.
- **One more hand-cited Windows constant**, `FILE_ATTRIBUTE_REPARSE_POINT`,
  read from `syscall` rather than hand-declared — the `x/sys` ban's standing
  cost (ADR 0018 §Consequences), paid here at its cheapest because the standard
  library already exports both constants this needs.

## Breaking changes

**Yes, one, and it is deliberate.** `NewFileLocker` acquisitions that
previously succeeded now fail with `LOCK_PATH_REDIRECTED` when the lock file's
path is a symbolic link (Unix) or a reparse point (Windows).

No signature changes, no configuration field, no removal. The public surface
gains exactly one symbol, `lock.LockPathRedirected`, which is additive.

A caller relying on the old behaviour was relying on the lock landing somewhere
other than where the locker said it was, which the measurements above describe.
There is no opt-out and none is offered: a flag to re-enable following would be
a flag to re-enable the defect, and the deployment that needs it is the one
that most needs to know.

## Alternatives considered

### Why not `Lstat` before the open

The check-then-open shape is a genuine TOCTOU: the planter creates the link
between the two calls and the open follows it anyway. `O_NOFOLLOW` and
`FILE_FLAG_OPEN_REPARSE_POINT` place the refusal *inside* the operation, which
is the only place it cannot be raced. `Lstat` survives in this change as
diagnosis after a refusal that has already happened — see D4.

`internal/service/writer/rotfile` is the SDK's prior art here and it does
**both**: `refuseSymlink` runs an `os.Lstat` first, and `openFlags` adds
`O_NOFOLLOW` so "a symlink planted between the Lstat check and this call fails
the open rather than silently redirecting". The pre-check buys it a nicer error
in the ordinary case. This domain declines it for one reason: the pre-check's
verdict would then be load-bearing in a reader's mind, and a reader who trusts
it stops asking whether the open is hardened — which is exactly how `rotfile`'s
own `openFlags` ends up `O_NOFOLLOW` on **Linux only** while darwin and the
four BSDs, which all have the flag, get the unhardened constant. Two mechanisms
for one property is how one of them quietly stops being maintained. (That
`rotfile` gap is noted here and not fixed here; a change about `lock` is the
wrong place for it.)

### Why not `os.Root` / `os.OpenRoot`

`vfs` uses `os.Root` and it is the natural neighbour to reach for. It confines
a path to a directory tree, which is a different property: since Go 1.25 a
symlink that stays *within* the root is followed, and the planted link in the
probe is inside the lock directory. It would not have refused this. It also
costs a `*os.Root` on the locker for the life of the process, which is a
lifetime question the domain does not otherwise have.

### Why not a hand-rolled `CreateFile` on Windows

It was the assumed shape until `go1.27`'s flag forwarding was read. Rejected on
the ADR 0081 §D5 argument above: restating the share mode by hand puts the
Windows directory verdict's only justification into a second place where it can
drift. See D2.

### Why not refuse every non-regular file

Tempting, symmetric with `vfs`'s `NotRegularFile`, and measured to be
unnecessary: a FIFO at the lock path already fails loudly through the ledger.
The one thing it would add over `O_NOFOLLOW` on Unix is catching a device node,
which is neither a split-brain nor silent. Declined as scope.

### Why not amend ADR 0081 rather than write this

Preferred at the outset, and rejected on ADR 0081's own sentence: "that is a
change to the domain's contract, not to this backend". It is cross-platform, it
allocates an error code, and it changes what `NewFileLocker` accepts — three
things a record about one backend should not be carrying. 0081's §Deferred item
is marked closed and points here, which is the amendment that *is* appropriate:
a status, not a decision edit (`docs/adr/CLAUDE.md` §Do NOT).

## Deferred

- **A symlink at a PARENT component of the lock path.** `O_NOFOLLOW` governs
  the final component only, measured on linux/amd64. Closing it means either
  resolving `FileConfig.Dir` and refusing a link anywhere in it — which refuses
  legitimate deployments where `/var/run` is a symlink to `/run` — or walking
  the path with `openat(2)` + `O_NOFOLLOW` per component, which needs a
  directory-handle API the SDK does not have and `x/sys` cannot supply it. The
  exposure is narrower than the closed one: the parents are the caller's own
  configuration, not a name this package derives.
- **A hard link at the lock path.** Accepted, see §Consequences. Refusing it
  means comparing `Stat.Nlink` after the open, which is one `fstat` — but
  `Nlink > 1` is also what a legitimate backup or deduplicating filesystem
  produces, and refusing a lock because a filer deduplicated it is a worse
  failure than the one it prevents. It would need a real measurement of how
  often that happens before it could be a refusal.
- **A DACL check for `checkDir` on Windows.** Untouched by this change and
  still open — ADR 0081 §Deferred. This change narrows what it would buy: the
  substitution it would prevent is now refused by the open on both platforms,
  so what remains for a DACL check is the denial-of-service half.

## References

- [ADR 0052 — the `lock` domain](0052-sdk-lock-domain.md)
- [ADR 0081 — the Windows file lock is a different primitive](0081-the-windows-file-lock-is-a-different-primitive.md)
- [ADR 0018 — cross-platform portability](0018-sdk-cross-platform-portability.md)
- [ADR 0056 — the `vfs` domain](0056-sdk-vfs-domain.md), `PathEscaped`
- [ADR 0054 — the `queue` domain](0054-sdk-queue-domain.md), `QueueDirectoryUnusable`
- [ADR 0015 — the logger writer taxonomy and rotation](0015-sdk-logger-writer-taxonomy-and-rotation.md), whose `rotfile` sink is the SDK's prior art for this open
- `open(2)` — `O_NOFOLLOW`: <https://man7.org/linux/man-pages/man2/open.2.html>
- `CreateFileW` — `FILE_FLAG_OPEN_REPARSE_POINT`: <https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilew>
- `GetFileInformationByHandle`: <https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-getfileinformationbyhandle>
- Creating symbolic links requires `SeCreateSymbolicLinkPrivilege`: <https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-createsymboliclinkw>
