# git (internal/service/vcs/git)

Implements the `core/vcs` contract by shelling out to the **git binary**.
Internal service implementation behind the public `pkg/v1/git` facade —
consumers import the facade, not this package.

## API

```go
func Resolve(ctx context.Context, cfg Config) corevcs.ResolutionValue
func GitDir(ctx context.Context, root string) (string, error)
func ShowFile(ctx context.Context, repoRoot, sha, relPath string) (string, error)
```

- `Resolve` computes what the branch changed. It returns **no error**: see below.
- `GitDir` resolves the directory holding HEAD and the index, memoized per root.
  It follows a `gitdir:` pointer file, so linked worktrees and submodules work.
- `ShowFile` reads a blob at a commit. A path absent at that commit is an error,
  which is what lets a caller tell "deleted" from "emptied".

## Resolve degrades, it does not fail

Every condition that prevents a trustworthy answer — no repository, a shallow
clone, an unresolved default branch, no merge-base, a git invocation that failed
or timed out — yields a `ResolutionValue` with `FullFallback` set and a `Reason`.
A partially collected set is discarded rather than returned.

"Nothing changed" and "I could not tell what changed" are opposite instructions
to a caller. A resolver that answered the second with an empty set would make a
scoped review pass on a branch it never examined.

## What the set spans

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

## Which files count is the caller's

`Config.Include` decides. Nil admits everything. A Go tool passes a `.go` suffix
test and a generated-file probe — which is what the source implementation
hard-coded, and what this package deliberately does not.

## Hostile repositories

`.git/config` travels with a clone and several keys make git execute a command
the repository chose. Every invocation goes through `runGitOutput`, which applies
`hardenedGitConfig`. See `CLAUDE.md` for what is hardened, what is deliberately
not, and the measurements behind both.

## Errors

| Sentinel | When |
|---|---|
| `corevcs.CommandFailed` | a git subcommand exited non-zero; the command line and stderr travel in `Private` |
| `corevcs.RepositoryUnresolved` | the root path could not be made absolute |

## See also

- `pkg/v1/git` — the public facade
- `internal/core/vcs` — the `ChangedSet` port and the value types
- ADR 0076 — the decision, its alternatives, and what it defers
