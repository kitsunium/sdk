<!-- updated: 2026-10-03T12:00:00Z -->
# internal/kernel/fs/

## Purpose

The kernel's filesystem family (ADR 0155 §2: the kernel takes a family where
one applies). This directory holds no Go code: it is a prefix, not a package,
and nothing imports `internal/kernel/fs` itself — which also keeps the name
from shadowing the standard library's `io/fs` in any file. Its members are
packages of the `internal/kernel` module, each with its own `CLAUDE.md`, and
each still passes the kernel's one rule — stdlib-only AND generic
(`internal/kernel/CLAUDE.md`).

## The rule that put them here

A package belongs here when what it does is MEASURE the filesystem — what a
path is made of, who a directory's mode or access list lets write it — or
perform ONE operating-system file primitive with no policy around it, and
return the kernel's own answer, never a verdict. The policy stays in the
domain: `/var/run` being a symbolic link is a distribution's decision and
`/tmp/myapp` being one may be an attack, so `lock`, `ipc` and `session` each
judge `pathchain`'s answer by their own rule (ADR 0083); `lock` accepts a DACL
it could not read and `queue` refuses one (ADR 0084 §D5, ADR 0095); `lock`
and `session` put different gates in front of the same file lock. A package
that decides, refuses or writes is a domain's, not the kernel's.

The family is a family rather than a place at the root because its kind is not
the root's: `errs`, `clock`, `backoff`, `semver` and `plugin` serve every family
or none in particular, while these answer questions about one resource, the
filesystem.

## Members

| Package | What it is | In `pkg/v1` |
|---|---|---|
| `flock/` | `TryLock(file)` / `Unlock(file)` / `Native` — an exclusive lock on a whole file, never a blocking call: `flock(2)` on the Unix kernels, `LockFileEx` over every byte on Windows, `errors.ErrUnsupported` elsewhere (ADR 0081, ADR 0159). The copy `lock` and `session` each carried | not published (ADR 0159 §4): a domain reaches it through `lock` |
| `pathchain/` | `Resolve(path)` — a path walked one component at a time over `os.Root` handles, each indirection reported with the mode of the directory that holds it: the measurement `O_NOFOLLOW` cannot give (ADR 0083) | not published (ADR 0159 §4): a measurement a domain applies with a policy, and alone it invites the policy-free use its own document warns against |
| `winacl/` | `GrantsAnyone(dir, onDirectory, onFilesWithin)` — does an identifier meaning anybody hold a right in a Windows directory's DACL, on the directory or on what its files inherit; the rights and the three masks (`ReplaceRights`, `CreateRights`, `ContentRights`) the SDK's rules ask (ADR 0084, ADR 0086). Moved from `lock`, which exported it for `queue` | not published, for the same reason as `pathchain` |

## Do NOT

- Put Go code in this directory, and above all not a package named `fs`: it
  would shadow `io/fs` wherever both are needed.
- Add a member that takes a decision. Measure (or call the kernel) here;
  refuse in the domain.
