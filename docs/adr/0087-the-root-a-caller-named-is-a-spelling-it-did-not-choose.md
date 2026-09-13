# ADR 0087 — the root a caller named is a spelling it did not choose, and a probe that did not answer is not an answer

- **Status**: Accepted
- **Date**: 2026-09-13
- **Deciders**: SDK maintainers
- **Related**: [ADR 0076](0076-what-a-branch-changed-is-a-value-that-can-say-it-does-not-know.md) (the `vcs` domain, whose §Deferred this closes), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a zero value is never an inert policy), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (the v0 licence, and why it is not used here)

## Context

ADR 0076 shipped the `vcs` domain with seven entries under §Deferred — the
largest untouched debt block in the repository. Each carried its own reasoning,
and the reasoning was sound in every case where it had been measured. This
record is what measuring the rest showed.

Four of the seven describe the SAME failure, arriving four ways: the resolver
answers, `Degraded()` is false, the set is not empty — and the query a caller
actually makes returns false for a file that did change. That is the one answer
ADR 0076 exists to refuse, and refusing it at the `Resolve` level did not refuse
it at the `Contains*` level.

One of the four was not in the list at all. It was found by asking a different
question of the hostile-`.git/config` surface: ADR 0076 derived its guard from
what a repository could make git EXECUTE. A repository can also make git
RENAME the `a/` and `b/` prefixes of a diff header, which executes nothing and
loses every line range.

## Decision

**Close six of the seven. Keep `Only git` open. Add one guard that was never on
the list.**

### 1. Both spellings of the root answer, and only the root

`git rev-parse --show-toplevel` canonicalises: given a path through a symbolic
link it answers with the link's target. Every recorded path is built from that
answer, and `Contains*` compares after `filepath.Clean`, which resolves nothing.
So a caller that reached the repository through a link queried a set keyed under
a root it never spells.

ADR 0076 answered this in the port comment — *query with paths resolved the same
way `Resolve` resolved the root* — and rejected both fixes it considered: an
`EvalSymlinks` per query, and canonicalising at construction, which "would still
not fix a caller whose own paths are lexical".

What that missed is which spelling belongs to whom. A caller chooses the paths
it builds; it does NOT choose the repository root, because it hands one in and
gets a different one back. `spelledTopLevel` derives the caller's own spelling
once — a single `EvalSymlinks`, and only when the hint is not already inside the
canonical root, so the ordinary case pays nothing — and every entry is recorded
under BOTH spellings while the set is built. Build time and not query time,
because the alias is known once at construction while queries are unbounded: a
caller asks `ContainsLine` once per diagnostic. That keeps the read path
byte-identical to what `internal/service/vcs/git/BENCH.md` measured — one map
lookup, no branch on the alias, zero allocations — where a per-query rewrite
would have put a `filepath.Join` allocation on every query of an aliased set and
invalidated the "zero allocations on every query" that report states. Neither
root is privileged. An indirection anywhere ELSE in a queried path is still
lexical, and the port comment now says which is which instead of saying all of
it is.

Reproduced before the change, on a repository behind a link with one edited
file: `Degraded()==false`, `IsEmpty()==false`, and `ContainsFile`, `ContainsLine`
and `ContainsDir` all false for that file.

### 2. A probe that did not answer degrades

`git rev-parse --is-shallow-repository` arrived in git 2.15. An older git fails
the invocation; any git could answer a third word. `isShallow` folded both into
false, and Resolve read false as full history and scoped a diff against a
merge-base it had no history for.

`shallowState` returns `(shallow, known)`. `known == false` degrades with a
Reason that names the probe. The two facts are different and only one of them
was ever safe to guess.

Reproduced with a `git` shim on PATH that fails only the shallow probe, and a
second that answers `maybe`: both produced `Degraded()==false` against a
repository whose history was intact — the shim proves what THIS package does
with a probe that will not answer, which is the fact under test.

### 3. `origin/HEAD` is verified in the invocation that reads it

`symbolic-ref --quiet refs/remotes/origin/HEAD` reports a symref whose target
was pruned exactly as happily as a live one. ADR 0076 called that "rare". It is
the state of every clone taken before its upstream renamed `master` to `main`,
until somebody runs `git remote set-head`.

`rev-parse --verify --quiet --abbrev-ref refs/remotes/origin/HEAD` resolves the
symref AND verifies its target exists, in one invocation — so the fix costs no
additional subprocess — and renders it as `origin/main`, the shape the fallback
candidate list already uses. The full ref path is passed rather than
`origin/HEAD` so rev-parse cannot be steered onto a `refs/origin/HEAD` a
repository planted higher in its search order.

Reproduced: a clone whose `origin/HEAD` points at the pruned
`refs/remotes/origin/master` degraded with *no merge-base with origin/master*
while `refs/remotes/origin/main` was present throughout.

### 4. The diff prefixes are pinned — the guard ADR 0076 did not look for

ADR 0076 derived its hardening list from what a repository could make git
execute. `diff.srcPrefix`, `diff.dstPrefix`, `diff.mnemonicPrefix` and
`diff.noprefix` execute nothing. They rename the `a/` and `b/` prefixes of a
unified-diff header, so `strings.TrimPrefix(raw, "b/")` leaves the repository's
own prefix in place and the line ranges are filed under `<root>/DST/x.go` or
`<root>/w/x.go`. Measured against a repository carrying each key, on the diffs
this package runs:

| key | `ContainsFile` | `ContainsLine` |
|---|---|---|
| `diff.srcPrefix=SRC/` + `diff.dstPrefix=DST/` | true | **false** |
| `diff.mnemonicPrefix=true` (index / working tree) | true | **false** |
| `diff.noprefix=true` | true | true |

`ContainsFile` survives all three because the NUL-separated name-status pass
carries no prefixes at all — the two-pass design of ADR 0076 doing exactly what
it exists for, and also the reason nothing else looked wrong. All four keys are
pinned to git's documented defaults with `-c`, which beats every config file.
`diff.noprefix` is pinned even though it survives by coincidence (`x.go` passes
the `b/` strip unchanged), because it OVERRIDES the two explicit prefixes:
neutralising them without it changes nothing, which was measured rather than
assumed.

### 5. `ShowFile`'s two refusals, classified on the failure path

`core/vcs.PathAbsent` was declared by ADR 0076 and never returned. Its two
rejected options were parsing git's stderr (locale-dependent) and "a second
`cat-file -e` probe per call".

The second option was costed in the wrong place. The probes belong on the
FAILURE path, which is already exceptional, so a successful read still costs one
subprocess and nothing else. It is two probes and not one, because `cat-file -e`
answers "no such object" identically for a commit that does not resolve and for
a path that is not in it: the commit is probed first, and only a readable commit
turns an absent object into `PathAbsent`. Anything else stays `CommandFailed`.

`runGitBlob` now returns its failure untyped, so the code is chosen once by the
caller that knows which refusal it is — previously the `CommandFailed` wrap
happened first and any re-labelling would have left `CodeOf` reporting the inner
code.

### 6. The `GitDir` memo is re-validated, not merely stored

ADR 0076: *a root that becomes a repository later is picked up, because failures
are not cached; a repository that MOVES within the life of a process is not.*

The first half is false exactly where it matters. It holds only when the first
call FAILED — and a root that lies inside a PARENT repository resolves
successfully, so `git init` in that root leaves the parent's git directory
memoized while a nearer repository governs it. Reproduced: `GitDir` returned
`<outer>/.git` where `git rev-parse --absolute-git-dir` in the same directory
returned `<outer>/sub/.git`.

The memo now carries two markers — the `os.Lstat` of `<root>/.git` at resolution
time, which may legitimately be absent, and the `os.Lstat` of the resolved
directory — and is served only while both still hold, compared by `os.SameFile`
so a re-created entry at the same name is correctly a different object. Measured
here: two lstats at ~2.9 µs against ~2.7 ms for the subprocess they stand in
for, so 99.9 % of what the memo was for is kept.

### 7. `Only git` stays open, and the question it asks gets an answer

A second implementation is a feature, not a debt, and writing one to discover
that a four-method port is portable would be a large change justified by its own
test. But the entry's question — which parts of the contract are git-shaped —
can be answered by inspection, and is: the port is four boolean queries carrying
no VCS vocabulary; `Resolve`, `Config`, `GitDir` and `ShowFile` are service-level
by ADR 0076's own decision, and `pkg/v1/git` is named for the implementation for
exactly that reason. The git vocabulary that did reach core is three FIELD names
on `ResolutionValue` — `BaseRef`, `BaseSHA`, `HeadSHA` — and renaming them is an
ADR 0040 shape change bought with nothing.

### 8. The copy entry closes with no code change, which is the finding

ADR 0076: *`git diff -M -C` reports `C### old new`, and both sides go through
`markPath`, so the unchanged `old` enters the set.* The mechanics are exactly
right and the consequence does not exist.

Without `--find-copies-harder` — which this package does not pass, and which no
configuration key enables — git only offers a copy whose SOURCE was modified in
the same changeset. So the source always carries its own record, and the
measured shapes show it:

| branch state | `--name-status -z -M -C` |
|---|---|
| source copied, source unmodified | `A b.go` — no copy record at all |
| source copied and modified | `M base.go` · `C100 base.go b.go` |
| source copied twice, source deleted | `C100 base.go b.go` · `R100 base.go c.go` |

In every shape the source is in the set for its own reason. The "one-line
change" would remove nothing — and if the measurement were wrong it would
under-report, which is the direction this domain must never move in. So the
over-report is documented as unreachable and the code is untouched, with
`TestACopysSourceIsAlreadyInTheSetForItsOwnReason` pinning the record shapes the
reasoning rests on: a future `--find-copies-harder` fails there first.

## Consequences

- A caller that points `Config.Root` at a symbolic link gets a changed set that
  answers. That is a behaviour change visible through `pkg/v1/git`, which is why
  this change carries `Release-bump: minor` (ADR 0007 §Bump semantics).
- `ShowFile` returns `CodePathAbsent` where it returned `CodeCommandFailed`. A
  consumer matching the latter for "file not in that commit" must move to the
  former; `errs.HasCode` is the matcher, and both codes were already exported.
- `Resolve` degrades in one situation where it used to answer — a shallow probe
  that fails — and answers in one where it used to degrade, a pruned
  `origin/HEAD`. Both move toward the correct verdict.
- `GitDir` costs two `os.Lstat` calls per hit where it cost none, and can now
  return an error for a root it once answered for, which is the point.
- No `pkg/v1` alias changes shape, so the ADR 0040 licence is not used. That is
  said out loud rather than left silent.

## Breaking changes

None at the type level: no exported signature or alias changes. Two behaviours
a consumer could have depended on do change, and both are named under
§Consequences — `ShowFile`'s code on an absent path, and `GitDir`'s refusal for
a root whose repository moved.

## Alternatives considered

- **Canonicalising every queried path.** The fix ADR 0076 rejected, and the
  rejection stands: one `EvalSymlinks` per `Contains*` call, on a method a
  linter calls once per diagnostic.
- **Rewriting the alias prefix per query instead of recording both spellings.**
  It works and costs no syscall, but it puts a `filepath.Join` allocation on
  every query of an aliased set, and BENCH.md asserts zero allocations on every
  query. The alias is a construction-time fact, so it is paid at construction.
- **Canonicalising the caller's paths instead of the root.** It cannot be done
  from inside the set: the caller may query a path that does not exist on disk
  (a deleted file is the ordinary case), and `EvalSymlinks` fails on those.
- **Refusing a `Config.Root` that traverses a link.** A refusal for a
  configuration that is correct everywhere else, and on macOS `/tmp` and on most
  Linux distributions `/var/run` are links the operating system ships.
- **Dropping the `GitDir` memo entirely.** Always correct, and ~2.7 ms per call
  on an API a daemon calls per request. The validation keeps the saving and
  removes the class of staleness that was reproducible.
- **Parsing git's stderr to classify `ShowFile`.** Rejected by ADR 0076 and
  rejected again for the same reason: the wording is localised and unversioned,
  so the refusal would change under `LC_ALL`.
- **Splitting rename from copy anyway, for clarity.** It would be a change whose
  "before" cannot be observed, which this repository does not land — and the
  only way it could matter is by removing a path from the set.

## Deferred

- **Only `git`.** Carried forward from ADR 0076 §Deferred unchanged, with the
  inspection above recorded beside it. A second implementation is a feature.
- **A repository created BETWEEN the root and the worktree top level.** The
  `GitDir` memo's two markers do not see it: the root's own `.git` is still
  absent and the memoized directory still exists. Only rev-parse's own discovery
  walk knows a nearer repository now wins, and reproducing that walk here is the
  thing this package refuses to do. The doc comment states the residue.
- **An indirection elsewhere in a queried path.** Only the root is aliased. A
  caller that builds a path through some other symbolic link chooses that
  spelling, and the port comment says the comparison is lexical.
- **`ResolutionValue`'s git-shaped field names.** `BaseRef`, `BaseSHA` and
  `HeadSHA` are git vocabulary in a package whose own CLAUDE.md forbids naming
  git. Renaming them is an ADR 0040 shape change; it waits for a second
  implementation to make it worth a consumer's recompile.

## References

- [ADR 0076](0076-what-a-branch-changed-is-a-value-that-can-say-it-does-not-know.md) — the `vcs` domain and the seven entries this record answers
- [ADR 0007](0007-sdk-release-and-versioning.md) §Bump semantics — why an observable internal change carries `Release-bump: minor`
- [ADR 0040](0040-changing-a-published-shape-while-v0.md) — the v0 licence, deliberately not used here
- `git-diff(1)` §`-C`, `--find-copies-harder` — https://git-scm.com/docs/git-diff
- `git-config(1)` §`diff.srcPrefix`, `diff.dstPrefix`, `diff.mnemonicPrefix`, `diff.noprefix` — https://git-scm.com/docs/git-config
- `git-rev-parse(1)` §`--abbrev-ref`, `--is-shallow-repository` — https://git-scm.com/docs/git-rev-parse
- `git-cat-file(1)` §`-e` — https://git-scm.com/docs/git-cat-file
