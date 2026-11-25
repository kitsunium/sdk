<!-- updated: 2026-10-05T00:00:00Z -->
# framework/internal/service/git/

## Purpose

Implements the `core/git` contract by shelling out to the **git binary** (ADR
0076). No VCS library: git's own porcelain is the contract. Stdlib plus
`framework/internal/core/git` and `internal/kernel/errs`.

It was the SDK library's `internal/service/vcs/git` until ADR 0158 moved the
domain into the framework, where its contract is named `git` too. It declares no
code of its own: it emits the core's `0.2.33.*` sentinels.

## Contents

| File | Role |
|---|---|
| `exec.go` | the hardened runner — `runGitOutput`, `runGitBlob`, `gitProbe`, `hardenedGitConfig`, `extDiffGuard` |
| `resolve.go` | `Resolve`, the four diff sources it folds together, `shallowState`, `spelledTopLevel` |
| `diff_parse.go` | unified-diff and `--name-status -z` parsing, each path filtered through `IncludeFunc` |
| `changed_set.go` | `ChangedSetValue`, the concrete `core/git.ChangedSet`, and its `spelledAs` rewrite |
| `changed_set_compliance.go` | the compile-time proof that `ChangedSetValue` satisfies `core/git.ChangedSet` |
| `config.go` | `Config` — where the repository is, and the caller's file filter (`IncludeFunc`) |
| `gitdir.go` | `GitDir`, memoized per root and re-validated on every call |
| `show.go` | `ShowFile` — a blob at a commit, or the two refusals |
| `head.go` | `Head` + `HeadValue` — HEAD's commit, its committer date, tracked changes; `committerTime` reads the raw commit object (ADR 0100) |

## How it works

This prose was the package's hand-written README. The README is now
written by `tools/genindex` from `docs/api`, as every framework package's is
(framework/CLAUDE.md rule 3), so it lives here.

### Resolve degrades, it does not fail

Every condition that prevents a trustworthy answer — no repository, a shallow
clone, an unresolved default branch, no merge-base, a git invocation that failed
or timed out — yields a `ResolutionValue` with `FullFallback` set and a `Reason`.
A partially collected set is discarded rather than returned.

"Nothing changed" and "I could not tell what changed" are opposite instructions
to a caller. A resolver that answered the second with an empty set would make a
scoped review pass on a branch it never examined.

### What the set spans

Four sources, because a review that ignored uncommitted work would contradict
what the author sees:

| Source | Command |
|---|---|
| the branch's contribution | `diff <merge-base> HEAD` |
| staged | `diff --cached` |
| unstaged | `diff` |
| untracked | `ls-files --others --exclude-standard` |

Renames and copies are detected (`-M -C`). A pure rename or a deletion marks the
file and its directory touched while contributing no line range.

### Which files count is the caller's

`Config.Include` decides. Nil admits everything. A Go tool passes a `.go` suffix
test and a generated-file probe — which is what the source implementation
hard-coded, and what this package deliberately does not.

### Hostile repositories

`.git/config` travels with a clone and several keys make git execute a command
the repository chose. Every invocation goes through `runGitOutput`, which applies
`hardenedGitConfig`. See `CLAUDE.md` for what is hardened, what is deliberately
not, and the measurements behind both.

### Errors

| Sentinel | When |
|---|---|
| `coregit.CommandFailed` | a git subcommand exited non-zero; the command line and stderr travel in `Private` |
| `coregit.RepositoryUnresolved` | the root path could not be made absolute |

### See also

- `framework/git` — the public facade
- `framework/internal/core/git` — the `ChangedSet` port and the value types
- ADR 0076 — the decision, its alternatives, and what it defers

## Rules from ADR 0087

Superseded by ADR 0154 (the charter); ADR 0087 stays as the incident's record, and its rules live here.

- **The caller's spelling of the root and git's canonical one both answer**,
  recorded at build time so a query stays one map lookup — the root only, never
  another link in a queried path.
- **A probe that did not answer degrades** (`shallowState` reports whether it
  KNOWS); it is never folded into a guess.
- **`origin/HEAD` is verified in the invocation that reads it**: `rev-parse
  --verify --quiet --abbrev-ref refs/remotes/origin/HEAD`, the full ref path, so
  a planted `refs/origin/HEAD` cannot steer it.
- **The diff prefixes are pinned** (`diff.srcPrefix`, `dstPrefix`,
  `mnemonicPrefix`, `noprefix`): keys that execute nothing and still misfile
  every line range.
- **`ShowFile` classifies on the failure path**, the commit probed before the
  path; the `GitDir` memo is re-validated by two `os.Lstat` and `os.SameFile`.
- *Lesson*: a repository reached through a link resolved, was neither degraded
  nor empty, and answered false for every edited file — under-reporting is the
  one direction this domain must never move in.

## Do NOT

- Add a git invocation that bypasses `runGitOutput`. The hardening is applied
  there, once.
- Return a partial `ChangedSetValue` from a failed collection. Degrade.
- Reintroduce a suffix or generated-file test in this package. It belongs to the
  caller, and `Test_parseNameStatus` pins both directions.
- Read a decision out of git's stderr. It is localised, and a refusal that
  depends on the operator's language is a refusal that changes under `LC_ALL`.
- Add `--find-copies-harder`. `-C` alone only offers a copy whose SOURCE was
  modified in the same changeset, so the source always carries its own record
  and the copy marking adds nothing — which is the whole of why ADR 0076
  §Deferred's copy entry closes without a code change.
  `TestACopysSourceIsAlreadyInTheSetForItsOwnReason` pins the record shapes that
  reasoning rests on.

## Verification

```sh
bazel test --config=race //framework/internal/service/git:git_test
(cd framework && GOWORK=off go test -race ./internal/service/git/...)
```

## Reference in NOTES.md

Moved unchanged to `framework/internal/service/git/NOTES.md`, under the same titles, and read only by a change that needs its mechanics: `Why-this-shape`; `Running against a repository you do not control`; `Two spellings of one root, and only two`; `A probe that did not answer is not an answer`; `Never a silent empty diff`; `Membership is resolved twice, on purpose`; `The file filter is the caller's`; `The memo is re-validated, not merely stored`; ``ShowFile` has two refusals and they mean different things`; ``Head` reads three facts and trusts none of them to `git log``.
