# ADR 0089 — A file that cannot cut a release cannot size one

- **Status**: Accepted; implemented in `scripts/release/cut-tags.sh`.
- **Date**: 2026-09-14
- **Deciders**: kitsunium maintainers
- **Amends**: [ADR 0007](0007-sdk-release-and-versioning.md) §2 (row 4 — the `Release-bump` trailer is scoped to the paths that can *cut* a release, not to `pkg/` alone)
- **Related**: [ADR 0085](0085-both-halves-of-a-release-read-the-same-range.md) (both halves read the same range), [ADR 0009](0009-pkg-public-module-resolvability.md) (detached release commit)

## Context

Two scripts decide a release. `compute-bumps.sh` answers **whether** to cut one; `cut-tags.sh` answers **how big**. ADR 0085 made them read the same *range*. They still do not read the same *paths*, and that difference produces two defects that point in opposite directions.

### The rule cannot express what the same table requires

ADR 0007 §2 states, in one table:

| Trigger | Bump |
|---|---|
| Behavioral change in `internal/**` observable through `pkg/<major>` | **minor**, not patch — semver §6 requires it |
| `Release-bump: minor` trailer on the merge commit **AND that commit touches `pkg/<major>/`** | minor on `<major>` |

Row 3 *requires* a minor for a behavioural change confined to `internal/`. Row 4 describes the only mechanism for asking for one, and gates it on touching `pkg/`. **There is no way to express what row 3 requires.**

`cut-tags.sh` implements row 4 faithfully — so the defect is in the table, not the script. Reproduced against the unmodified script, with a detached base release as in production:

```
commit touching only internal/service/svc.go, message ending:
  Release-bump: minor

  trailer parsed by git : 'minor'
  DRY-RUN: would tag chain: … pkg/v0.1.1
```

Git reads the trailer. The release is cut — all four modules are tagged. The size falls back to patch, with no message and no refusal.

This is not theoretical. A release tags the whole module graph at one version — observed on `main`: `internal/{core,kernel,service}/v0.3.5` alongside `pkg/v0.3.5` — and a release range routinely spans several merges: ADR 0085 measured 23 of this repository's 44 ranges covering more than one commit, the largest 213. So an `internal/` change is published whenever it shares a range with anything that cuts one, which is the ordinary case rather than the exception. It ships; only its size could not be stated.

`compute-bumps.sh:80-102` also cuts a release for an `internal/`-only change on its own, when `bazel query rdeps(//pkg/..., //internal/<X>/...)` is non-empty — and that path demonstrably works: #213 (`732cdb2`) touched **zero** `pkg/v*/` paths and seven under `internal/service/entitlement/`, and `pkg/v0.3.6` was cut for it. So an `internal/`-only change reaches consumers, at a size its author could not state.

It is, however, **intermittent**, and this ADR deliberately does not lean on it. Fifteen minutes after #213, #214 (`c707de5`) changed four non-documentation files under `internal/service/codec/ndjson/` — the same module root, so literally the same query — and cut **no tag at all**; `pkg/v0.3.7` does not exist. Which of the two answers was the real one cannot be recovered, because the script discards the query's stderr (`bazel query … 2>/dev/null` feeding `grep -q .`) and never reads its exit code, so a failed query and an empty result are the same value. That was a separate defect, of the same family as the ones above and worse in degree — not a wrong size but no release at all — and it is **fixed on `main`** by #233 (`9a87c5b`), which captures the exit code and refuses loudly instead of reporting "no release". Measured against a `bazel` stub exiting 7, same range, same directory: the pre-#233 script → **exit 0, silent**; the post-#233 script → **exit 1**, `refusing to report 'no release' for a computation that did not run`. Nothing in this ADR changes because of it: the argument never leaned on the rdeps path. Nothing here depends on it: range-sharing alone makes `internal/` changes publishable, and that is measured.

**The trailer is a modifier, not a trigger** — observed in production, on `main`, by a second path that did not go through the reproduction above. The merge commit of #213 carries **no** `Release-bump:` trailer at all: its last paragraph is `Refs: #192`, and `%(trailers:key=Release-bump,valueonly,separator=%x1F)` returns empty. `pkg/v0.3.6` was cut regardless, by the `next_patch` default. So whatever the path scope does, it does not decide *whether* something is published — only how large the number is on something that ships either way. That is what makes the `pkg/` restriction a limit on honesty rather than on capability.

### And the scope it does enforce protects nobody

Row 4's prose justifies the `pkg/` restriction on security grounds:

> Minor is gated by a trailer that an attacker cannot smuggle through a PR body — […] it is honored only for the majors whose paths that commit touches.

That protection does not exist. The repository merges with `squash_merge_commit_message: COMMIT_MESSAGES`, so GitHub composes the squash message from the **branch commits' own bodies** — the contributor's text, not the maintainer's. `cut-tags.sh` says so itself:

> the squash message GitHub composes embeds the BRANCH COMMITS' own bodies, so reading anywhere would let a contributor set the release size

And the path scope is one line away from being bypassed. Reproduced against the unmodified script:

```
commit touching only pkg/v1/foo/CLAUDE.md, message ending:
  Release-bump: minor

  DRY-RUN: would tag chain: … pkg/v0.2.0
```

A documentation file sets the size of a release. The same file cannot *cut* one: `compute-bumps.sh:70` excludes `*/CLAUDE.md` and `*/BUILD.bazel` explicitly, because "its churn alone must not cut a release". `cut-tags.sh` never learned that exclusion.

So the two scripts hold **different notions of a file that counts**, and the gap is absurd in both directions: a file that cannot trigger a release can size one, while a change that must be a minor cannot ask to be.

The residual effect of the `pkg/` scope is therefore not to stop an attacker — one line in any `CLAUDE.md` under `pkg/` does that — but to stop an **honest** request from being expressed. A control that filters only people acting in good faith.

## Decision

**One notion of a file that counts, shared by both halves.**

A commit's `Release-bump` trailer is in scope when that commit touches at least one path that **could cut a release**:

- under `pkg/` or under `internal/`,
- excluding `*/CLAUDE.md` and `*/BUILD.bazel` — the same exclusion `compute-bumps.sh` already applies.

Concretely, in `cut-tags.sh`, both the trailer-counting test and the misplaced-trailer refusal move from `^pkg/` to that predicate.

This amends ADR 0007 §2 row 4 to read: *`Release-bump: minor` trailer on a first-parent commit in the range AND that commit touches a path that can cut a release* → minor.

## Consequences / Semantics

**The net is a hardening, not a relaxation.** The two halves must be read together:

- widening to release-relevant paths makes row 3 expressible for the first time;
- excluding `*/CLAUDE.md` and `*/BUILD.bazel` closes the cheapest route an attacker had.

After both, sizing a release requires touching code that actually cuts one. Today, a single documentation line suffices. The capability to *cause* a publication is unchanged — `compute-bumps.sh` already grants it to `internal/`-only changes — so widening grants nothing new; it only lets the size be stated honestly for something that ships either way.

**The two changes are correct only together, and the history shows it.** Commit `3f681412` (`feat(resilience)`, #185) touched `pkg/v1/resilience/CLAUDE.md` *and* three `.go` files under `internal/service/resilience/`, and carries the text `Release-bump: minor` — at **line 99 of a 126-line message**, so `%(trailers:key=Release-bump,valueonly,separator=%x1F)` returns **empty** and git parses nothing. What the two changes rescue together is therefore not the value but the **misplaced-trailer refusal**. Measured on the three configurations, against this commit: `^pkg/` alone → counts → refusal raised; the exclusion **alone** → its only `pkg/` path is a `CLAUDE.md`, so nothing counts → **refusal silenced, silent fall back to patch**; widening + exclusion → counts through `internal/` → refusal raised. Shipping the exclusion without the widening would have re-created issue #217 — a lost bump, silently — inside the fix for #218. Shipping one without the other would have moved the defect rather than removed it.

**Doc-only releases lose the ability to size themselves.** A range whose only `pkg/` content is `CLAUDE.md`/`BUILD.bazel` churn cannot request a minor. This is intended and not a loss: such a range does not cut a release at all under `compute-bumps.sh:66-73`, so there is no release for it to size.

## Breaking changes

None for consumers. Tag shape, module paths and the patch default are unchanged. A maintainer who previously sized a release by touching a `CLAUDE.md` under `pkg/` must now touch a path that cuts one.

## Alternatives considered

### Why not delete row 3 instead

Row 3 invokes semver §6. Deleting it would record that this SDK does not do correct semantic versioning, in a document that cites the clause requiring it. That abandons the rule rather than its implementation.

### Why not keep `^pkg/` and accept that `internal/` cannot be sized

That is today's behaviour, and it is exactly the defect: a change that ships and alters observable behaviour is published at a size its author could not choose. The failure is silent — the same shape as the two `Release-bump` trailers lost to placement (issue #217), reached by a different route.

### Why not run `bazel query rdeps(//pkg/..., //internal/<X>/...)` per commit

`compute-bumps.sh` verifies that an `internal/` change actually reaches `//pkg/...` before cutting. `cut-tags.sh` deliberately does **not** repeat that query per trailer-bearing commit: it would add one Bazel invocation per candidate to a step that today needs no build graph, and the answer cannot change the outcome much. `cut-tags.sh` runs only when `compute-bumps.sh` has already emitted `pkg` — the release *is* being cut; the only question is its size. The approximation is permissive on `internal/`, and strictly less permissive than today's rule, which accepts a documentation file under `pkg/`.

### Why not tighten the smuggling surface here too

The real anti-smuggling control is that the trailer is read from main's **first-parent** line only (ADR 0085), not from the commits a merge brought in. What remains is that a squash message is composed from contributor text. Narrowing that is a change to the merge configuration, not to the path scope, and it is out of scope for this ADR — recorded below.

## Deferred

- **`squash_merge_commit_message: COMMIT_MESSAGES` lets contributor text reach the trailer parser.** Setting it to `BLANK` or `PR_TITLE` would force a maintainer to compose the release-sizing message. Not changed here: it alters every merge in the repository, not just release sizing, and deserves its own decision.
- **Per-commit rdeps verification for `internal/` paths**, if a case ever shows a trailer sized a release through an `internal/` package that does not reach `//pkg/...`.

- **This ADR does NOT close issue #220, and the shared notion is the reason.** "One notion of a file that counts" is shared by both halves, but that notion still **counts test files**. `counts_for_release` excludes `*/CLAUDE.md` and `*/BUILD.bazel` and nothing else, so a 100 % `_test.go` diff counts. Measured against the real commit that cut `pkg/v0.4.1` — `8addc948` (#222), two files, both `_test.go`, both under `internal/service/crypto/pbkdf2pw/`, zero files outside `internal/`, zero non-test files:

  | rule | verdict on `8addc948` |
  |---|---|
  | `^pkg/` (before this ADR) | does not count |
  | `counts_for_release` (this ADR) | **counts** |

  So this ADR *widens* what may size that release rather than stopping it. That is consistent with its own reasoning — `compute-bumps.sh` cut the release either way, and only the size was at stake — but it must not be read as a fix. The trigger is untouched: `compute-bumps.sh` is **byte-identical** to `main` in this change set. Reproduced end to end on the exact release range `02f436e8..8addc948` (baseline `pkg/v0.4.0^1`, one commit): `compute-bumps.sh --dry-run` prints `pkg`. Negative control on `8addc948..91f80a6` (`.github/workflows/` only): prints nothing, exit 0.

- **The reduction to the module root is what loses the answer, and Bazel already has it.** `awk -F/ '/^internal\//{print $1"/"$2}'` maps both changed files to `internal/service`, then `rdeps(//pkg/..., //internal/service/...)` returns **230 targets**. At the granularity of the change itself the same query returns **`INFO: Empty results`** — `rdeps(//pkg/..., //internal/service/crypto/pbkdf2pw:pbkdf2pw_test)` → 0 lines, and each of the two changed source-file targets → 0 lines. The build graph distinguishes a test target from a library target; the script discards that distinction one line before asking. This is a route to #220 that needs **no hand-maintained category list**, which is what makes it worth recording here rather than only in the issue. Not taken in this change set, and the trap it carries **changed shape while this PR was in flight** — which is why it is written here with its date rather than as a standing fact. A source file not yet declared in a `BUILD.bazel` makes the query **exit 7** with `ERROR: no such target`. Before #233 that was indistinguishable from an empty result under `2>/dev/null` + `grep -q .`, so a per-file route would have cut **no release at all** for any newly added file — silently. Since #233 (`9a87c5b`) the same exit code is **loud**: measured with a `bazel` stub exiting 7 on the range `02f436e8..8addc948`, the pre-#233 script gives **exit 0 and no output**, the post-#233 script gives **exit 1** and `refusing to report 'no release' for a computation that did not run`. So the remaining obligation is not to avoid a silent miss but to carry a structural guard of its own, the per-file analogue of #233's `find "$modpath" -name BUILD.bazel -print -quit` — otherwise every commit adding a file before `make gazelle` fails the release job instead of releasing nothing.

- **Two git parsers disagree about the same message, and only one is on the release path.** A body line consisting of `---` makes `git interpret-trailers --parse` treat everything after it as a patch and return an **empty** trailer list. `cut-tags.sh` does not use that route: it reads `%(trailers:key=Release-bump,valueonly,separator=%x1F)` through `git log --format`, which does **not** apply the patch-separator rule, and its misplaced-trailer guard counts `^Release-bump:` in `%B` without calling `interpret-trailers` at all. Measured on git 2.47.3 with a message whose last paragraph is `Release-bump: minor` preceded by a `---` line: `interpret-trailers --parse` → empty, `%(trailers:…)` → `minor`, `grep -cE '^Release-bump:'` on `%B` → `1`. The two routes the script uses **agree**, so no false refusal and no lost value: the release chain is immune. Recorded as a divergence and not as a defect, because an ADR whose subject is one shared notion should say where two notions still exist even when neither is currently wrong. It becomes live the day anything on this path is rewritten in terms of `interpret-trailers`.

## References

- [ADR 0007](0007-sdk-release-and-versioning.md) §2 — bump semantics (amended by this ADR, row 4)
- [ADR 0085](0085-both-halves-of-a-release-read-the-same-range.md) — both halves of a release read the same range
- `scripts/release/compute-bumps.sh:66-102` — the notion of a file that counts, and the rdeps rule
- `scripts/release/cut-tags.sh` — `counts_for_release`, and its two call sites
- Issue #218 — the reproduction this ADR closes
- Issue #217 — the same visible failure reached through trailer placement
- Issue #220 — a test-only diff cuts a release: NOT closed by this ADR, and §Deferred says why with the measurement
