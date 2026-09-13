# internal/kernel/pathchain/

## Purpose

The SDK's path-resolution primitive. Stdlib-only, domain-neutral. `Resolve`
walks a path one component at a time and reports what each component IS —
a real directory or an indirection — together with the mode of the directory
that component was found in. It is the measurement `O_NOFOLLOW` cannot give,
because `O_NOFOLLOW` governs the FINAL component only and a link planted at a
parent is traversed by every open whatever flags it carries (ADR 0083).

`internal/service/lock` is its first consumer: ADR 0082 closed the final
component and named the parents as deferred, on the grounds that closing them
"needs a directory-handle API the SDK does not have". This is that API.

## Contents

| Symbol | Surface | Use case |
|---|---|---|
| `Resolve` | `Resolve(path string) ([]StepValue, error)` | audit every component of a path whose identity is a security property |
| `StepValue` | `Path` / `Name` / `Container` / `Mode` / `Target` / `Indirect` | one component, described rather than traversed |

## Conventions

- **It refuses nothing.** A refusal needs a policy and a policy needs a domain.
  The two callers this was built for want opposite verdicts on the same shape —
  `/var/run` being a symbolic link is a distribution's decision, `/tmp/myapp`
  being one may be an attack — and the difference lives in
  `StepValue.Container`, never in the link.
- **A missing component is an answer, not a fault.** The walk stops there and
  returns a nil error, because the caller may be auditing a directory it is
  about to create. Every other failure is the `*os.PathError` the filesystem
  produced, unwrapped, so one caller wraps it once in its own vocabulary.
- **`Container` is read by `fstat` on a handle**, not by a second lookup of a
  path string, so the mode belongs to the directory the component was actually
  found in.
- **`Path` reflects the indirections already followed.** Given
  `/pub/app -> /srv/app`, walking `/pub/app/locks` describes the third
  component as `/srv/app/locks`, because that is where it lives and where the
  question "who could have replaced it" has to be asked.
- **Loops are refused by the KERNEL.** Past `maxHops` the whole path is handed
  to `os.Stat` and its answer is returned, so the errno is the platform's own
  (`ELOOP` on Linux and Darwin, `EMLINK` on FreeBSD and DragonFly, `EFTYPE` on
  NetBSD) rather than a three-value table that is wrong on the seventh kernel.
- **No error codes.** The package emits none and owns no dotted-quad range.

## Why `os.Root` and not `syscall.Openat`

Read in the pinned toolchain rather than assumed: go1.27's `syscall` package
declares `Openat` for **linux, aix and wasip1 only**
(`src/syscall/syscall_linux.go`, `zsyscall_aix_ppc64.go`, `fs_wasip1.go`).
darwin does not define `SYS_OPENAT` at all and reaches the kernel through libc
trampolines internal to the standard library. `golang.org/x/sys`, which would
supply it, is banned SDK-wide (ADR 0018).

`os.Root` is the standard library's own `openat` walk and exists on every GOOS.
The cost of borrowing it is that it RESOLVES a symbolic link whose target stays
inside the root and REFUSES one whose target leaves it — neither of which is
what a walk wants. So this package never asks `os.Root` to traverse an
indirection: it detects one with `Lstat`, reads it with `Readlink`, and
restarts the walk itself. `os.Root` descends into components already known to
be real directories, and nothing else.

## Do NOT

- **Read a verdict into `Indirect` alone.** It is true for `/tmp` on macOS.
  The policy is `Indirect` AND something about `Container`.
- **Use it as a guard on a hot path.** It is an audit taken once, at
  construction; a component replaced after it returns is not seen by it. A
  component an attacker can PREDICT must be guarded at the open instead —
  `O_NOFOLLOW` and `FILE_FLAG_OPEN_REPARSE_POINT`, as `internal/service/lock`
  does for the lock file's own name.
- **Add a refusal here.** See §Conventions; the first one would have to pick
  one of two incompatible policies and would be wrong for the other caller.
- **Replace the `os.Stat` in `exhausted` with a constant.** `syscall` does not
  define `ELOOP` on every GOOS this package compiles for — plan9 is the one
  that proves it — and the three kernels that do disagree on the value.

## Verification

```sh
bazel test --config=race //internal/kernel/pathchain:pathchain_test
# or: cd internal/kernel && GOWORK=off go test -race ./pathchain/...
```

The suite plants symbolic links and skips, naming the reason, where an
unprivileged account cannot create one — Windows without
`SeCreateSymbolicLinkPrivilege`. The lane that runs it on a Windows kernel is
the `windows` job of `.github/workflows/e2e-cross.yml`.
