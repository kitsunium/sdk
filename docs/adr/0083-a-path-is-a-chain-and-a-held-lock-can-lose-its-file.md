# ADR 0083 — a path is a chain, and a held lock can lose its file

- **Status**: Accepted
- **Date**: 2026-09-13
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0082](0082-the-lock-path-is-a-file-never-a-link-to-one.md) §Deferred — both of its first two items are closed here, one by prevention and one by detection, and the ADR says which is which
- **Related**: [ADR 0052](0052-sdk-lock-domain.md) (the domain), [ADR 0081](0081-the-windows-file-lock-is-a-different-primitive.md) §D5 (the directory rule, and the shared-group arrangement it deliberately accepts), [ADR 0018](0018-sdk-cross-platform-portability.md) (the build bar and the `x/sys` ban), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (a new primitive goes in the layer that owns it), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) (the code allocation)

## Context

ADR 0082 closed the substitution an attacker reaches through the lock file's
own NAME — a name this package derives and therefore makes predictable. Its
§Deferred recorded two things it did not close. Both were reproduced again
against [`c41c9b3e`](https://github.com/kitsunium/sdk/commit/c41c9b3e), the commit
that carries 0082, before anything here was written.

### Exposure 1 — the entry can be unlinked while it is held

`checkDir` accepts `0777|sticky`, by name, because that is what `/tmp` is. In
such a directory the account that created the lock file OWNS that entry, so the
sticky bit permits them to unlink it — and not only when the lock is free:

```
répertoire trwxrwxrwx : ACCEPTÉ
victime détient le verrou : fence=1 inode=69831
2e verrou AVANT l'échange : held=false err=<nil> (attendu false)
entrée désliée pendant que la victime la détient
2e verrou APRÈS l'échange : held=true err=<nil> inode=69832
SPLIT : deux détenteurs, fences 1 et 1, inodes 69831 et 69832
Extend de la victime : <nil>
```

Two holders, two inodes, and the fence RESET rather than advanced — both report
1. The last line is the one that decides this ADR: the victim asked whether it
still held the lock, and was told yes.

### Exposure 2 — `O_NOFOLLOW` governs the final component only

```
Dir demandé  = …/pub/myapp/locks
composant planté = …/pub/myapp -> …/elsewhere
construction ACCEPTÉE
acquisition sur parent planté : err=<nil>
SUIVI : le verrou a atterri sur …/elsewhere/locks/a4d268….lock (mode=-rw-------)
fence remis par la cible du lien = 1
```

The whole lock directory moves. Every lock, every ledger, and `checkDir`'s
verdict is taken on the target rather than on the directory the operator
configured — so an attacker who redirects into a tidy `0700` directory of their
own also passes the mode rule.

ADR 0082 deferred this on a specific ground, and it is the ground this ADR has
to answer: closing it means "walking the path with `openat(2)` + `O_NOFOLLOW`
per component, which needs a directory-handle API the SDK does not have and
`x/sys` cannot supply it."

## Decision

### D1 — A new kernel primitive, `pathchain`, because the missing thing is generic

`internal/kernel/pathchain` resolves a path one component at a time and returns
one `StepValue` per component: what it is, where it points, and **the mode of
the directory it was found in**.

It goes in the kernel because it satisfies the only admission rule there has
ever been — stdlib-only AND generic (ADR 0074, ADR 0002's kernel gate). Nothing
in its signatures names a lock, a directory role or a policy: it takes a path
and returns components. `queue` refuses a symlinked state directory today with
its own hand-rolled check, `vfs` has `PathEscaped`, and `rotfile` has
`refuseSymlink` — three domains asking a neighbouring question and none of them
able to ask this one.

**It refuses nothing, deliberately.** A refusal needs a policy and a policy
needs a domain, and the two callers in view want opposite verdicts on the same
shape: `/var/run` being a symbolic link is a distribution's decision,
`/tmp/myapp` being one may be an attack. The difference is in `Container`,
never in the link, so the primitive measures and the domain decides.

A component that does not exist ends the walk with a **nil error**, because the
caller audits a directory it is about to create.

### D1b — The path is made absolute WITHOUT being cleaned

`filepath.Abs` is the obvious call and it is the wrong one. It `Clean`s, and
`Clean` removes `link/..` **lexically**, while the kernel follows the link and
only then takes the parent step. Those are different directories whenever the
link does not point at a child of its own container — so a walk over the
cleaned path would audit somewhere other than where the caller's open lands,
which is the single failure this package exists to prevent.

So an absolute path is used verbatim and a relative one is concatenated with
the working directory. `..` is not a problem to be normalised away: the walk
applies it as a movement through the directory handles it already holds, which
is exactly what the kernel does. The one shape that cannot be kept verbatim is
a Windows drive-relative path (`C:foo`), which has no expansion but the lexical
one, and it is named in the code rather than silently folded in.

`TestResolveAppliesParentAfterTheLinkAndNotBefore` builds a tree where BOTH
answers exist as real directories, so a regression is a wrong path rather than
a missing component, and uses `filepath.EvalSymlinks` as the oracle because it
implements the kernel's semantics and is not the code under test.

### D2 — It is built on `os.Root`, and that was READ rather than assumed

The obvious primitive is `syscall.Openat`. In the pinned toolchain (go1.27.0)
`syscall` declares it for **linux** (`src/syscall/syscall_linux.go:285`),
**aix** (`zsyscall_aix_ppc64.go:638`) and **wasip1** (`fs_wasip1.go:531`) — and
nowhere else. darwin, freebsd, openbsd, netbsd and dragonfly have no
`syscall.Openat`; darwin does not even define `SYS_OPENAT`, because it reaches
the kernel through libc trampolines internal to the standard library. A
hand-rolled walk would have served one of the six kernels `e2e-cross` runs and
silently skipped five.

`os.Root` is the standard library's own `openat` walk and it exists on every
GOOS. Its Unix implementation is
`unix.Openat(parent, name, O_NOFOLLOW|O_CLOEXEC|O_DIRECTORY)`
(`src/os/root_unix.go:118`); its Windows implementation passes
`windows.O_NOFOLLOW_ANY` (`src/os/root_windows.go:147`).

Borrowing it costs one thing, and it is the thing ADR 0082 rejected `os.Root`
for: a `Root` RESOLVES a symbolic link whose target stays inside it and REFUSES
one whose target leaves it, and neither is what a walk wants. So `pathchain`
never asks `os.Root` to traverse an indirection. It detects one with `Lstat`
(an `fstatat` with `AT_SYMLINK_NOFOLLOW`, relative to a handle), reads it with
`Readlink`, and restarts the walk itself at the target. `os.Root` descends into
components already known to be real directories, and nothing else.

`..` is a stack pop rather than a lookup, because `Root.OpenRoot("..")` is
`path escapes from parent` by construction — that is what a `Root` is for.

### D3 — A parent indirection is refused only when anybody could have planted it

`checkChain` runs in `prepareDir`, **before** `os.MkdirAll`, because `MkdirAll`
follows a planted parent and auditing afterwards means refusing the directory
only after having created it inside the attacker's tree. The refusal is
`LOCK_PATH_REDIRECTED`, the sentinel ADR 0082 allocated, naming the component,
the configured directory and the target.

The rule is: **an indirection at a component is refused when the directory
holding it is world-writable, and accepted otherwise.** The sticky bit is NOT
an exemption, which is what makes this rule different from `checkDir`'s — and
the difference is exactly ADR 0082's own argument, applied one level up: sticky
governs UNLINKING an entry that exists, and planting a component CREATES one at
a name nobody has taken.

The alternative — refuse a link anywhere in the path — is wrong, and it is
wrong on a platform this repository tests on. That was written from
documentation and is now **measured**: on the `macos-arm64` job of
`e2e-cross`, every `t.TempDir()` resolves through `/var -> /private/var`, which
Apple ships. `/private/tmp` is the same story one directory over; `/var/run` is
one to `/run` on most Linux distributions; `C:\Users\All Users` is a junction
to `C:\ProgramData`.

A blanket refusal would turn every lock directory under any of them into
`LOCK_PATH_REDIRECTED`, which is ADR 0018 §(a)'s failure mode wearing an error
that blames the deployment for the operating system's own layout. The rule as
written accepts them, and that was measured on the same run: every path in
`chain_posix_test.go` reaches `t.TempDir()` through Apple's `/var` link, and
all four accepting rows passed, because the directory holding `/var` is `/` and
nobody but root can write it.

The four accepting rows of `TestAnIndirectionAboveTheLockFileIsRefusedOnly...`
exist for the reason ADR 0081 §D5's table has its own: a guard that refuses the
obvious case while quietly refusing a legitimate one is a different bug wearing
the same green tick.

### D4 — On Windows no parent component is refused, and the reason is named

`plantable` answers "no" on Windows, so `checkChain` refuses nothing there.
`os.Stat` synthesises the permission bits from one attribute,
`FILE_ATTRIBUTE_READONLY`, so every writable directory reports `0777` and the
rule would refuse every junction under one.

The question has an answer on that platform — the directory's DACL, an ACE
granting `FILE_ADD_FILE` or `FILE_ADD_SUBDIRECTORY` to Everyone (S-1-1-0) or
Authenticated Users (S-1-5-11) — and it is the same answer ADR 0081 §Deferred
and ADR 0082 §Deferred both name. It is deferred once more, in §Deferred below,
with what it would take. `TestAnIndirectionAboveTheLockFileIsAcceptedOnWindows`
pins the gap on a real kernel so that it is a measured decision rather than an
untested assumption, and so that the DACL change has a test to flip.

What remains uncovered on Windows is narrower than it sounds: the FINAL
component, the one this package derives and an attacker can predict, is refused
by the open (ADR 0082 §D2). The components above it are the caller's own
configuration.

### D5 — `LOCK_FILE_REPLACED` (`0.3.51.5`): the lock is not kept, the holder is TOLD

A held lock whose file is no longer the file its name leads to is reported, at
`Acquire` and at `Extend`, through a new sentinel carrying `EX_CONFIG` (78).

**This is detection, not prevention, and the ADR refuses to describe it as
anything else.** The entry is unlinked after the open, by an account the
directory's permissions genuinely allow to unlink it, and the victim's
descriptor keeps working because a descriptor outlives its name on every
kernel. The second holder really does get the lock —
`TestTheSplitIsDetectedAndNotPrevented` asserts that it does, on purpose, so
that no reader can come away believing the exclusion was restored.

What changes is that the victim finds out. `Extend` is the only call a holder
makes DURING the section, so it is where the check lives, and `Keepalive`
turns it into a cancelled context whose cause is the loss — origin wins on the
wrap trail (ADR 0005), so `LOCK_FILE_REPLACED` and `LOCK_KEEPALIVE_LOST` are
both recoverable from it.

Two remedies were considered and prevent nothing, which was established rather
than assumed:

- **An owner check on the lock file.** Rejected in ADR 0082 §Deferred and still
  rejected, on its own ground: it breaks the arrangement `checkDir`
  deliberately accepts — "a lock shared between two service accounts through a
  common group is a deliberate arrangement" (ADR 0081 §D5) — in which the entry
  belongs to the OTHER account by design.
- **`O_EXCL` on creation plus a recorded identity.** `O_EXCL` answers whether
  THIS process created the file. It says nothing about who unlinks it
  afterwards, and the unlink is the entire attack — it happens later, on a
  descriptor already opened and locked. It would have added a second open path
  and prevented nothing.

The only prevention is a lock directory no other account can write, which is
what `NewFileLocker` creates (`0700`) when the directory is absent. That is now
said in three places rather than implied.

### D6 — The comparison is `os.SameFile`, with no platform split, and it fails OPEN

`sameEntry` compares `file.Stat()` (an `fstat` on the descriptor that IS the
lock) against `os.Lstat(path)`. `os.Lstat` rather than `os.Stat`, because an
indirection planted at the name after the open is precisely the replacement
being looked for, and following it would compare the descriptor against the
planter's chosen target and find them equal.

It answers NO only when that can be positively established — the name is gone,
or it resolves to a different file. **Every inconclusive answer is reported as
YES**, because a lock that starts refusing renewals on a stat hiccup fails its
caller harder than the attack it watches for.

`os.SameFile` is the portable spelling: `(dev, ino)` on Unix, the volume serial
plus the file index on Windows, where the standard library loads them by
opening the path with a desired access of **zero** — a request Windows does not
subject to the sharing check, which is why it works on a file this process
holds open without `FILE_SHARE_DELETE` and under a mandatory whole-file
`LockFileEx` range (ADR 0081). That last sentence is the one thing in this
change that could not be reasoned about safely from documentation, so
`TestExtendKeepsSucceedingOnAHeldLock` measures it on the `windows` job of
`e2e-cross` — if it were wrong, every renewal on that platform would fail,
which is worse than the exposure.

The check is NOT run on `Release`. Releasing is closing a descriptor this lease
owns; it succeeds whatever the name now points at, and a `Release` that
returned an error would break `defer lease.Release(ctx)` at the one moment a
caller is unwinding.

### D7 — The acquisition-time check closes a narrower, real window

Between `openLockFile` and the `flock` there is a window in which the entry can
be unlinked and recreated, leaving this descriptor locking an inode nothing
names. Two syscalls close it, and they are the same two. It is reported as
`LOCK_FILE_REPLACED` with `op=Acquire`, and the deferred abandon in `takeFlock`
gives the flock and the descriptor back on the way out.

## Consequences / Semantics

- **A deployment that worked now fails, by design — twice.** A lock directory
  reached through a symbolic link planted in a world-writable directory is
  refused at construction. A held lease whose file has been unlinked refuses
  its next `Extend`. Both are in §Breaking changes.
- **`Extend` can now fail for a lease this process still holds.** The previous
  doc comment said it could not, and it was true for the world it described —
  nothing can take a held `flock` away. It was not true for the world where the
  file's NAME can be taken away instead.
- **`checkDir`'s rule is unchanged**, mode table included. `checkChain` is a
  second, stricter rule about a different question, and the two are deliberately
  not merged: one is about unlinking an entry, the other about creating one.
- **`FileConfig.Dir` may still be a symbolic link** when the directory holding
  it is not world-writable — which is what `/var/run/myapp` and macOS's
  `/tmp/myapp` are.
- **A hard link at the lock path is still accepted**, unchanged from ADR 0082
  §Deferred: it is not an indirection, and `Nlink > 1` is also what a
  deduplicating filer produces.
- **A component's reported path is where it LIVES, not how the caller spelled
  it**, and two platforms make that visible in opposite directions — both
  measured on `e2e-cross` rather than anticipated. macOS resolves
  `/var -> /private/var`, so a reported path is *longer* than the one passed
  in. GitHub's Windows runner hands out `TMP` as an 8.3 SHORT name, so a
  reported path *keeps* `RUNNER~1` where `filepath.EvalSymlinks` would expand
  it — `pathchain` reports the components it was given, because a short name is
  a second directory entry for one inode rather than an indirection, and
  `os.Root` does not expand it either. Three platforms spell one directory
  three ways, so every "did the walk land here" assertion in both suites
  compares `os.SameFile` over `os.Lstat` and never strings. The first two
  attempts at those assertions compared strings and were red on one platform
  each; that history is in the test files, beside the assertions.
- **Cost.** One path walk per `NewFileLocker` — a handful of `openat` and
  `fstatat` calls, once, at construction. Two extra syscalls per `Acquire` and
  per `Extend`. `Extend` is a keepalive's cadence, not a hot path.
- **The dependency graph is untouched**: no module added to any `go.mod`, no
  `go.sum` entry, no `MODULE.bazel` change, and the new primitive is
  stdlib-only. `pkg` consumers inherit nothing.
- **`internal/kernel/pathchain` joins the `e2e-cross` lane** as `KERNEL_PKGS`.
  What it measures IS the platform — `os.Root` is `openat(2)` on one kernel and
  `O_NOFOLLOW_ANY` on the other, a junction and a symbolic link are one bit here
  and two mechanisms there, and a volume root is only a path on one of them.

## Breaking changes

**Yes, two, and both are deliberate.**

1. `NewFileLocker` now refuses a `Dir` any component of which is an indirection
   planted in a world-writable directory, with `LOCK_PATH_REDIRECTED`. Unix
   only; on Windows nothing new is refused (D4).
2. `Lease.Extend` on a file lease now returns `LOCK_FILE_REPLACED` when the
   lock file has been unlinked or replaced, where it previously returned nil.
   A caller that ignored `Extend`'s error is unaffected in the sense that
   nothing panics, and is exactly the caller this exists for.

The public surface gains one symbol, `lock.LockFileReplaced`, which is
additive. No signature changes, no configuration field, no removal, and no
opt-out: a flag to re-enable following a planted parent is a flag to re-enable
the defect, and a flag to silence `LOCK_FILE_REPLACED` is a flag to be lied to.

## Alternatives considered

### Why not resolve `Dir` once and pin a directory handle

It is the natural companion to the walk and it closes a different problem: a
component swapped AFTER the audit. It was declined here for two reasons. The
lock file's open would then have to be `openat(dirfd, base, O_NOFOLLOW|…)`,
which `syscall` cannot express on darwin or the BSDs (D2) — and the one
expression available, `os.Root.OpenFile`, RESOLVES a within-root symlink,
which is strictly weaker than the `O_NOFOLLOW` the package already has. Buying
a pinned handle at the price of un-hardening the final component is the wrong
trade, and doing it on Linux only is the platform asymmetry ADR 0082 refuses.

### Why not refuse a `0777|sticky` lock directory outright

It would close exposure 1 by prevention rather than detection. It also refuses
`/tmp`, which ADR 0081 §D5's table accepts by name, and `/tmp/ktn-linter-$UID`
is where a real consumer's daemon puts its instance lock when
`XDG_RUNTIME_DIR` is unset. Refusing it would move that consumer to a directory
it would have to create and mode itself — which is the right advice and not
something the SDK can impose retroactively on every caller.

### Why not put the walk in `internal/service/lock`

That is where it was going to go, and ADR 0074's rule sent it down a layer: a
primitive goes in the layer that OWNS it, and nothing about resolving a path
belongs to the lock domain. `queue`, `vfs` and `rotfile` all ask neighbouring
questions with hand-rolled checks; burying this one inside `lock` would make it
the fourth private answer rather than the first shared one.

### Why not make `pathchain` return a verdict

It would remove `checkChain` entirely and cost the primitive its generality.
The policy `lock` needs — refuse an indirection whose container is
world-writable, sticky bit irrelevant — is not the policy `vfs` needs, and it
is not even the policy `checkDir` needs one line later in the same function.

### Why not emit a typed error code from `pathchain`

A kernel package with its own dotted-quad range is normal here (`ring`,
`batcher`). It would have bought one thing: a named sentinel for an indirection
loop. Instead the loop refusal hands the whole path to `os.Stat` and returns
the KERNEL's answer, so the errno is the platform's own — `ELOOP` on Linux and
Darwin, `EMLINK` on FreeBSD and DragonFly, `EFTYPE` on NetBSD. That also keeps
the package compiling on plan9, where `syscall.ELOOP` does not exist at all;
the first draft referenced it and broke the ADR 0018 build bar, which is how
this was found rather than reasoned.

## Deferred

- **A DACL check for `checkDir` and `plantable` on Windows.** Named in ADR 0081
  §Deferred, again in ADR 0082 §Deferred, and not closed here either — but it
  is now load-bearing for two rules rather than one, so it is worth stating
  precisely what it needs. `syscall` in go1.27 exports `StringToSid`,
  `LookupSID`, `GetLengthSid` and the `SID` type, so the well-known SIDs cost
  nothing; what is missing is `GetNamedSecurityInfoW` and `GetAce`, both
  advapi32 exports bindable with `syscall.NewLazyDLL` exactly as ADR 0081 bound
  `LockFileEx` from kernel32, plus hand-declared `ACL`, `ACE_HEADER` and
  `ACCESS_ALLOWED_ACE` layouts and an `EqualSid` comparison. That is the whole
  of it. It is deferred here for review size rather than for cost, and it is
  the next change on this package.
- **`plantable` reads other-write and not other-execute.** Creating an entry in
  a POSIX directory needs BOTH `w` and `x`, so a `0702` container is not in
  fact plantable by a stranger and the rule refuses it anyway. That is
  deliberate and it is the conservative direction, but the reason it is not
  changed here is consistency rather than correctness: `checkDir`'s table
  refuses `0702` on the identical reasoning and is pinned by ADR 0081 §D5, so
  the two rules would disagree about the same mode. Changing both is a
  different change with its own table.
- **A component replaced AFTER `checkChain` returns.** The audit is taken once,
  at construction. Closing it needs the pinned directory handle §Alternatives
  declines, and that in turn needs a portable `openat` the standard library
  does not expose.
- **`Release` does not report a replaced file.** Deliberate (D6), and the cost
  is that a process which only ever releases — never extends — learns nothing.
  A `Keepalive` is the supported way to be told.
- **A hard link at the lock path.** Unchanged from ADR 0082 §Deferred.

## References

- [ADR 0082 — the lock path is a file, never a link to one](0082-the-lock-path-is-a-file-never-a-link-to-one.md)
- [ADR 0081 — the Windows file lock is a different primitive](0081-the-windows-file-lock-is-a-different-primitive.md)
- [ADR 0052 — the `lock` domain](0052-sdk-lock-domain.md)
- [ADR 0018 — cross-platform portability](0018-sdk-cross-platform-portability.md)
- [ADR 0074 — what a public alias may point at](0074-what-a-public-alias-may-point-at.md)
- `go1.27.0` `src/os/root_unix.go`, `src/os/root_windows.go` — `os.Root`'s component walk
- `go1.27.0` `src/syscall/syscall_linux.go`, `zsyscall_aix_ppc64.go`, `fs_wasip1.go` — every declaration of `syscall.Openat`
- `go1.27.0` `src/os/types_windows.go` — `IO_REPARSE_TAG_SYMLINK` and `IO_REPARSE_TAG_MOUNT_POINT` both map to `ModeSymlink`; `loadFileId` opens with a desired access of zero
- `open(2)` — `O_NOFOLLOW` governs the final component: <https://man7.org/linux/man-pages/man2/open.2.html>
- `path_resolution(7)` — why a parent component is traversed regardless: <https://man7.org/linux/man-pages/man7/path_resolution.7.html>
