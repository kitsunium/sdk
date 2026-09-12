# internal/service/vcs/git/

## Purpose

Implements the `core/vcs` contract by shelling out to the **git binary** (ADR
0076). No VCS library: git's own porcelain is the contract. Stdlib plus
`internal/core/vcs` and `internal/kernel/errs`.

## Contents

| File | Role |
|---|---|
| `exec.go` | the hardened runner — `runGitOutput`, `gitProbe`, `hardenedGitConfig`, `extDiffGuard` |
| `resolve.go` | `Resolve` and the four diff sources it folds together |
| `diff_parse.go` | unified-diff and `--name-status -z` parsing, and `IncludeFunc` |
| `changed_set.go` | `ChangedSetValue`, the concrete `core/vcs.ChangedSet` |
| `config.go` | `Config` — where the repository is, and the caller's file filter |
| `gitdir.go` | `GitDir`, memoized per root |
| `show.go` | `ShowFile` — a blob at a commit |

## Why-this-shape

### Running against a repository you do not control

`.git/config` travels with a clone, and several of its keys make git execute a
command the repository chose. `hardenedGitConfig` neutralises them, and the file
documents what was **demonstrated** rather than assumed — against a repository
with the key set to a script, this package's own invocations executed it 5
times before the guard and 0 after:

- `core.fsmonitor` — git runs it to enumerate changed paths, on status and diff.
  Disabled with `-c`, which beats every config file, so the target repository
  cannot re-enable it.
- `diff.external` — git runs it instead of its own diff. This one is NOT
  disabled with `-c`: setting it empty makes git try to execute `""` and abort,
  breaking diff outright. The documented lever is `--no-ext-diff`, injected
  per-subcommand by `extDiffGuard` because other subcommands reject the flag.
- `core.hooksPath` — no subcommand here runs a hook, and it is set anyway so the
  guarantee does not depend on that list staying read-only.

Deliberately NOT hardened, to avoid hardening against nothing: `core.pager`
(tested — git detects the non-TTY and skips paging), `diff.<driver>.textconv`
(did not fire on any invocation this package makes), and the network-only keys
(`credential.helper`, `core.sshCommand`, `protocol.*`), since nothing here talks
to a remote.

### Never a silent empty diff

Every failure path in `Resolve` produces `FullFallback` with a Reason. A partial
changed set would under-report, and under-reporting is the one answer that lets a
caller skip code it should have examined. A git invocation that fails or times
out mid-collection discards what it had and degrades.

### Membership is resolved twice, on purpose

`addDiff` runs the `--name-status -z` pass FIRST and the unified diff second. The
`-z` format is NUL-separated and never c-quoted, so a path with a tab, a newline
or non-ASCII bytes arrives verbatim; the unified-diff header for that same file
may be unresolvable. The first pass is therefore authoritative for membership and
the second only adds line ranges — a file the parser cannot header-match is still
file-touched, never dropped.

### The file filter is the caller's

`IncludeFunc` replaces what the source implementation hard-coded: a `.go` suffix
test plus a generated-file probe. Those are one caller's policy — a changed set
over Markdown is just as legitimate — so the caller states it. A nil filter
admits everything, which is the safe direction per ADR 0031: including too much
widens a scope, while a default that dropped files would under-report.

## Do NOT

- Add a git invocation that bypasses `runGitOutput`. The hardening is applied
  there, once.
- Return a partial `ChangedSetValue` from a failed collection. Degrade.
- Reintroduce a suffix or generated-file test in this package. It belongs to the
  caller, and `Test_parseNameStatus` pins both directions.

## Verification

```sh
bazel test --config=race //internal/service/vcs/git:git_test
go test -race ./internal/service/vcs/git/...
```
