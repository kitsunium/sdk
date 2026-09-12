# ADR 0076 — what a branch changed is a value that can say it does not know, and running git against a repository you do not control is hardened

- **Status**: Accepted
- **Date**: 2026-09-12
- **Deciders**: SDK maintainers
- **Related**: [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a zero value is never an inert policy), [ADR 0052](0052-lock-domain.md) (why a two-backend domain has no registry), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (which layer a public alias points at), [ADR 0075](0075-reading-the-cgroup-cap-that-already-bounds-us.md) (the first versement from the same source)

## Context

A tool that acts on a diff — a review gate, a scoped test selection, a
changelog — needs to know what the current branch changed relative to where it
started. Getting that right is more subtle than `git diff`:

- the branch's contribution is the **three-dot** delta from the merge-base, not
  from the default branch's tip;
- staged, unstaged and untracked work all count, because a review that ignored
  uncommitted changes would contradict what the author is looking at;
- renames and copies must be detected, and a pure rename or a deletion touches a
  file without contributing a single changed line;
- a path with a tab, a newline or non-ASCII bytes defeats the unified-diff
  header format, and silently loses its hunks.

And then there is the part that is not about diffs at all. `.git/config` travels
with a clone, and several of its keys make git execute a command the repository
chose. A tool pointed at a repository it did not write is running attacker input.

Two failure modes matter more than the rest, and both are silent:

1. **A resolver that cannot tell what changed, answering "nothing changed".**
   Those are opposite instructions. A scoped review that receives an empty set
   passes — on a branch it never examined.
2. **A read-only query executing code.** Against a repository with
   `core.fsmonitor` set to a script, the source implementation's own invocations
   (`diff -U0 -M -C`, `status --porcelain`) ran it 5 times.

The implementation adopted here comes from `kodflow/ktn-linter`'s `pkg/git`,
where both were found and fixed, with the hardening documented against
measurements rather than assumptions.

## Decision

**A new `vcs` domain, whose contract is a changed set and whose one
implementation shells out to git.**

`internal/core/vcs` holds the `ChangedSet` port (frozen at four methods),
`LineRangeValue`, and `ResolutionValue`. `internal/service/vcs/git` implements
it. `pkg/v1/git` is the facade — named for the implementation, as `cgroup`,
`rlimit` and `sdnotify` are, because that is what a caller is actually getting.

Three decisions carry the weight:

### 1. Degrading is a value, not an error, and never an empty set

`Resolve` returns no error. Every condition that prevents a trustworthy
answer — no repository, a shallow clone, an unresolved default branch, no
merge-base, a git invocation that failed or timed out mid-collection — produces a
`ResolutionValue` with `FullFallback` set and a `Reason` a caller is meant to
surface. A partially-collected set is discarded rather than returned.

Per ADR 0031, the zero value is neither readable shape: nil `Set` **and**
`FullFallback` false is a combination no constructor mints, so a caller who
forgot to check `Degraded()` cannot read "nothing changed" out of a value nothing
produced.

### 2. Which files count is the caller's policy, not the domain's

The source implementation hard-coded a `.go` suffix test and a generated-file
probe. Those are one caller's policy; a changed set over Markdown, or one that
keeps generated files, is just as legitimate. `Config.Include` is where the
caller states it, and a nil `Include` admits everything.

Nil-admits-all is the safe direction in the ADR 0031 sense. Including too much
widens a scope, which a caller notices; a default that silently dropped files
would under-report a diff, which is the one answer this domain must never give.

### 3. Every invocation is hardened, and the hardening is scoped to what was demonstrated

`core.fsmonitor` and `core.hooksPath` are neutralised with `-c`, which beats
every config file so the target repository cannot re-enable them.
`diff.external` is NOT: setting it empty makes git try to execute `""` and abort,
breaking diff outright, so `--no-ext-diff` is injected into the subcommands that
honour it — and only those, since the others reject the flag.

Equally recorded is what is **not** hardened, so the guard is not mistaken for a
sandbox: `core.pager` (tested — git detects the non-TTY and skips paging),
`diff.<driver>.textconv` (did not fire on any invocation this package makes), and
the network keys (`credential.helper`, `core.sshCommand`, `protocol.*`), since
nothing here contacts a remote. Adding a subcommand to this package means
re-deriving that list.

## Consequences

- A consumer gets a correct changed set — four sources, rename and copy
  detection, exotic filenames — without re-deriving the git invocations, and
  without a VCS library in `pkg/go.mod`, which stays dep-light.
- Membership is resolved twice and the order matters: the `--name-status -z`
  pass is authoritative, because NUL separation is never c-quoted, and the
  unified-diff pass only adds line ranges. A file whose diff header cannot be
  parsed is still file-touched. Reversing the order would reintroduce silent
  drops.
- `ContainsFile` can be true where `ContainsLine` is false for every line — a
  deletion or a pure rename. That is stated in the port comment, because the
  other reading is the one a caller assumes.
- The SDK now depends on a `git` binary being on PATH for this one package. It
  is an optional package: nothing else in the SDK imports it, and absence of git
  degrades rather than panics.
- `ContainsDir` is not recursive, and is named `Dir` rather than the source's
  `PackageDir`: the Go vocabulary was the caller's, not the domain's.

## Breaking changes

None. `vcs` is a new domain in this change set: no existing symbol changes shape,
and nothing else in the SDK imports it.

## Alternatives considered

- **A fuller VCS abstraction** — repositories, commits, refs, history. There is
  one implementation and no second one in view; a contract broader than the
  implementation describes nothing and constrains everything. ADR 0052's
  registry argument applies to the name too: a `vcs.Open("hg")` resolving from a
  config string would let a typo swap implementations with every call still
  succeeding.
- **`(ChangedSet, error)`.** It is the Go-idiomatic shape and it is wrong here.
  A caller that treats `err != nil` as "skip the scoping" and one that treats it
  as "fail the run" are both defensible, and the type does not say which. The
  degraded value says exactly what to do: treat everything as in scope, and
  surface Reason.
- **Keeping the `.go` filter in the package**, with an option to disable it.
  That makes one caller's policy the default and everyone else's an opt-out, on
  a package with no reason to prefer Go.
- **A go-git dependency instead of shelling out.** It would remove the PATH
  requirement and the hostile-config surface. It would also add a large
  dependency to a module whose whole point is that consumers inherit nothing,
  to reimplement porcelain git already computes correctly.

## Deferred

- **A path reached through a symlink does not match.** `Resolve` stores paths at
  git's canonical top-level while the `Contains*` methods compare with
  `filepath.Clean`, which is LEXICAL. A caller querying a path that traverses a
  symlink gets false for a file that did change — the dangerous direction, since
  it under-reports. Canonicalising on every query would cost an `EvalSymlinks`
  syscall per call; canonicalising once at construction would still not fix a
  caller whose own paths are lexical. Stated in the port comment instead: query
  with paths resolved the same way `Resolve` resolved the root.
- **A COPY marks its source touched.** `git diff -M -C` reports `C### old new`,
  and both sides go through `markPath`, so the unchanged `old` enters the set.
  It over-reports, which is the safe direction, and it is the source
  implementation's behaviour. Splitting rename from copy is a one-line change
  and a semantics change, so it is recorded rather than slipped into a
  versement.
- **A failed shallow probe reads as "not shallow".** `git rev-parse
  --is-shallow-repository` failing makes `isShallow` answer false, so a shallow
  checkout whose probe is unsupported gets a scoped set instead of the full
  fallback. The code comment already said so; it is now recorded as a decision.
- **A stale `origin/HEAD` is not re-checked against the candidate list.**
  `resolveBaseRef` trusts a successful `symbolic-ref` without verifying the
  commit still exists, so a stale symbolic ref degrades where `origin/main`
  would have worked. Fail-safe (everything in scope), and rare.

- **Only `git`.** No second implementation, and therefore no way to know which
  parts of the contract are git-shaped. `ChangedSet` is deliberately narrow so a
  second one would have little to disagree with, but the claim is untested.
- **`ShowFile` does not separate absent from failed.** `core/vcs.PathAbsent`
  exists and is not yet returned: telling the two apart means parsing git's
  stderr, which is locale-dependent, or a second `cat-file -e` probe per call.
  The sentinel is declared so the distinction has a name when it is implemented.
- **`GitDir`'s memo is never invalidated.** A root that becomes a repository
  later is picked up, because failures are not cached; a repository that MOVES
  within the life of a process is not. For a tool that resolves once at startup
  this is the right trade, and the comment says so rather than implying the memo
  is coherent.

## References

- [ADR 0031](0031-policy-zero-values-are-never-inert.md) — why the zero `ResolutionValue` is neither readable shape
- [ADR 0052](0052-lock-domain.md) — the no-registry argument this reuses
- [ADR 0074](0074-what-a-public-alias-may-point-at.md) — why `Config` aliases the service and `ChangedSet` aliases core
- `gitrepository-layout(5)` — https://git-scm.com/docs/gitrepository-layout
- `git-diff(1)` §`--name-status`, `-z` — https://git-scm.com/docs/git-diff
- `proc(5)` §`/proc/[pid]/mountinfo` — https://man7.org/linux/man-pages/man5/proc.5.html
