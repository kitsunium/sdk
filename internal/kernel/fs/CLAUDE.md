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

## Five directory rules, deliberately not one

Five service domains keep state in a directory whose identity is a security
property, and each refuses a directory by a rule of its own. They share the
MEASUREMENTS — `pathchain`, `winacl`, the standard library's `Stat` and
`Lstat` — and nothing else, on purpose. A single parameterised "private
directory" check was proposed by the reorganisation audit and is refused:
every difference below is a decision with a reason, and a shared function
would make one domain's decision every domain's, weakening whichever was
strictest without a test noticing (ADR 0083, ADR 0084, ADR 0148). Each
package's `CLAUDE.md` states its own column and why.

| | `app/lock` | `proc/ipc` | `security/secret` | `security/session` | `data/queue` |
|---|---|---|---|---|---|
| The directory is | the lock's SCOPE, shared on purpose | a private socket's whole access control | the secrets, and their names | the session records | the messages, one per entry |
| Created as | `MkdirAll` `0700` | `Mkdir` `0700` (its parent must exist) | `MkdirAll` `0700` | `MkdirAll` `0700`, then `fchmod 0700` on the held handle | root `MkdirAll` `0700`; each state `Mkdir` `0700` |
| An existing one refused when (Unix) | anybody can REPLACE an entry: other-write without sticky | group- or other-WRITABLE (`0o022`), a link, not a directory, or another account's | ANY group or other bit (`0o077`), read included | any bit beyond `0700`, judged on the HELD directory, and the path must still name it | root: other-write without sticky; a state: other-write, sticky or not, or a link |
| Group-writable | accepted — a lock shared by two service accounts is the point | refused | refused | refused | accepted — a queue shared through a group, likewise |
| The path above it | `pathchain`: an indirection whose holder anybody can write (sticky exempts nothing); Windows: `CreateRights` in the holder's DACL | `pathchain` where anybody can write the holder: an indirection, a component neither this account's nor root's, or any component without sticky; plus the socket directory's own holder | the lock domain's audit, run by the file locker it opens over the same directory — after its own `MkdirAll`, so a refused path may already hold the new directory | `pathchain`, lock's rule (Unix) | not audited |
| Too wide | refused, never narrowed | refused, never narrowed | refused, never narrowed | narrowed only if this call created it; otherwise refused | refused, never narrowed |
| Windows | the DACL (`winacl`): `ReplaceRights` + inherited `ContentRights`; an unreadable list ACCEPTED and logged | no directory: a named pipe with its own DACL | refused (`vfs` refuses the platform) | refused (no owner-only DACL is built, no directory flush) | the DACL (`winacl`); an unreadable list REFUSED (`unverifiable`) |

What IS shared is shared at the level where it is identical: the walk
(`pathchain`), the DACL reader (`winacl`), the file lock (`flock`). Two
fragments are identical between domains and are left where they are, each a
single bit test whose meaning — who counts as "anybody" — is the domain's
rule: `session`'s Unix `plantable` is `lock`'s, by `session`'s own choice
(ADR 0083), and `queue`'s root rule makes `lock`'s `checkDir` decision with a
different rendering of the mode. Neither is worth a kernel symbol that would
couple two policies.

## Do NOT

- Put Go code in this directory, and above all not a package named `fs`: it
  would shadow `io/fs` wherever both are needed.
- Add a member that takes a decision. Measure (or call the kernel) here;
  refuse in the domain.
- Merge the five rules above into one, here or in a shared service package.
  Extract only what is identical in behaviour — and a mask, a mode bit or a
  fail-open default is not, once two domains have chosen differently.
