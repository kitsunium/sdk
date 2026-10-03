<!-- updated: 2026-10-03T03:20:00Z -->
# internal/kernel/fs/

## Purpose

The kernel's filesystem family (ADR 0155 §2: the kernel takes a family where
one applies). This directory holds no Go code: it is a prefix, not a package,
and nothing imports `internal/kernel/fs` itself — which also keeps the name
from shadowing the standard library's `io/fs` in any file. Its member is a
package of the `internal/kernel` module with its own `CLAUDE.md`, and it still
passes the kernel's one rule — stdlib-only AND generic
(`internal/kernel/CLAUDE.md`).

## The rule that put it here

A package belongs here when what it does is MEASURE the filesystem — what a
path is made of, what a directory's mode allows — and return the measurement,
never a verdict. The policy stays in the domain: `/var/run` being a symbolic
link is a distribution's decision and `/tmp/myapp` being one may be an attack,
so `lock`, `ipc` and `session` each judge `pathchain`'s answer by their own
rule (ADR 0083). A package that decides, refuses or writes is a domain's, not
the kernel's.

The family has one member. It is a family rather than a place at the root
because its kind is not the root's: `errs`, `clock`, `backoff`, `semver` and
`plugin` serve every family or none in particular, while `pathchain` answers
questions about one resource, the filesystem. A second primitive that measures
the filesystem joins it here.

## Members

| Package | What it is | In `pkg/v1` |
|---|---|---|
| `pathchain/` | `Resolve(path)` — a path walked one component at a time over `os.Root` handles, each indirection reported with the mode of the directory that holds it: the measurement `O_NOFOLLOW` cannot give (ADR 0083) | not published (ADR 0159 §4): a measurement a domain applies with a policy, and alone it invites the policy-free use its own document warns against |

## Do NOT

- Put Go code in this directory, and above all not a package named `fs`: it
  would shadow `io/fs` wherever both are needed.
- Add a member that takes a decision. Measure here; refuse in the domain.
