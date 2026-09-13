# ADR 0085 — Both halves of a release read the same range

**Status**: Accepted; implemented in `scripts/release/`.
**Date**: 2026-09-13
**Deciders**: kitsunium maintainers
**Amends**: ADR 0007 §2 (the `Release-bump` trailer is no longer read from the merge commit *only*)
**Related**: ADR 0009 (detached release commit — why the baseline is the tag's first parent)

## Context

A release is decided by two scripts that never speak to each other. `compute-bumps.sh` answers **whether** to release, by diffing a *range* — everything added to `main` since the commit the last release was cut from. `cut-tags.sh` answers **how big**, and it read a *single commit*: `git log -1 … HEAD`, both for the `Release-bump` trailer and for the paths that scope it.

ADR 0007 §2 wrote that rule as "the trailer is read from the merge commit only", which was true of a world where one merge produces one release. That is not this repository. A release fires on `workflow_run` with `conclusion == 'success'`, and the CI concurrency group cancels an in-flight run when the next push lands: a cancelled run is not a success, so the commit it belonged to gets **no release at all**. Its changes are not lost — the next successful run's range still contains them — but under the old rule its *trailer* was, because the trailer was only ever sought at `HEAD`.

Two facts make that more than a corner case.

**Ranges are routinely longer than one commit.** Measured over this repository's own 44 consecutive release ranges on `main` — each one `<previous tag>^1..<next tag>^1` — **23 span more than one commit**, the largest 213. A one-commit range is not the normal case; it is barely half of them.

**It fired.** Two merges twelve seconds apart, the first carrying `Release-bump: minor` and touching `pkg/`, the second carrying neither. `compute-bumps.sh` saw the first commit's `pkg/` change inside the range and asked for a release; `cut-tags.sh` read the trailer from the second and found none, so `bump_for_pkg` fell to its default. Fifty new public symbols would have shipped as a patch. Nothing failed and nothing warned; a maintainer noticed and re-ran the cancelled CI by hand. See <https://github.com/kitsunium/sdk/issues/201>.

The same asymmetry had quietly disabled a refusal. `Release-bump: major` is rejected **by name** from a `v1` base (ADR 0009 — a breaking v2 needs a real `…/pkg/v2` module path). Sitting one commit behind `HEAD`, that trailer was not rejected; it was rounded down to a patch.

## Decision

1. **One baseline, one function.** `release_base()` moves into the shared `scripts/release/lib/tag-format.sh`: the latest stable `pkg` tag's **first parent** — the `main` commit that release was cut from, since the release commit itself is detached and unreachable from `HEAD` (ADR 0009) — falling back to the root commit, and to nothing at all on a repository of one commit. Both scripts call it. The asymmetry is removed structurally rather than by keeping two copies in step.

2. **`cut-tags.sh` reads the trailer over `release_base()..HEAD`** — the same commits `compute-bumps.sh` diffs. It also accepts `--range=<rev>..HEAD`, as `compute-bumps.sh` already did, so the two can be pinned together.

3. **The scope stays per commit.** A `Release-bump` trailer counts only for a commit that itself touched `pkg/` — ADR 0007 §2's rule, unchanged in substance and now applied commit by commit rather than to `HEAD` alone. Scoping the range as a whole would let any commit's paths vouch for any other commit's trailer, which is exactly the smuggling the rule exists to prevent.

4. **The largest bump in the range wins**: `major` > `minor` > anything else, and anything unrecognised ranks with patch. A trailer states what the release must be *at least*, so a later commit that says nothing cannot shrink one that did, and two commits both asking for minor coalesce into the one minor they both meant.

5. **A commit's repeated trailers are split** on the separator `%(trailers:…,separator=,)` joins them with, and ranked as though they were separate commits. Previously `minor,major` matched neither arm of the `case` and fell to patch.

6. **`Release-bump: major` keeps its ADR 0009 refusal** from a `v1` base — and now actually reaches it.

7. **Provenance is stated, on stderr.** When the winning trailer did not come from `HEAD`, `cut-tags.sh` says which commit it came from. That is the visible trace that a release was sized by a commit whose own CI run produced none. It is stderr and never stdout: stdout is the tag list the workflow feeds to `gh release create`.

## Consequences / Semantics

| Situation | Before | After |
|---|---|---|
| Trailer on `HEAD`, touching `pkg/` | minor | minor (unchanged) |
| Trailer one commit behind `HEAD` | **patch** | minor |
| Trailer on a commit touching no `pkg/` | patch | patch (unchanged) |
| Several commits, no trailer | patch | patch (unchanged) |
| `minor` and `major` both in range | **patch** | major |
| `major` behind `HEAD`, `v1` base | **patch, silently** | refused (ADR 0009) |
| Trailer already honoured by the last release | patch | patch (unchanged) |

- **A cancelled CI run no longer costs anything but time.** Whatever commit finally carries a successful run, the range behind it still contains the cancelled commit *and its trailer*.
- **A trailer is never honoured twice.** `release_base()..HEAD` opens strictly after the commit the previous release was cut from, so the trailer that sized `pkg/v0.2.0` is outside the range that sizes the next tag.
- **The cost is two `git log` invocations per trailer-bearing commit**, and one for the range. Commits carrying a trailer are rare; commits are cheap to walk.
- **What is *not* fixed**: an `internal/` change observable through `pkg` still needs the trailer on a commit that also touches `pkg/`. ADR 0007 §2 says so and this ADR does not move it — widening the scope to "any commit in the range" would let a trailer written for one subsystem size a release for another.

## Breaking changes

One, and it is a refusal becoming reachable rather than a new rule. `Release-bump: major` from a `v1` base now **fails the release job** (exit 1, the ADR 0009 message) in the case where it previously cut a silent patch — when that trailer sits behind `HEAD`. A release that used to succeed with the wrong size now stops and says why. There is no `v1` tag on this repository yet, so no release in existence changes shape.

## Alternatives considered

### Why not refuse to cut instead

The [issue](https://github.com/kitsunium/sdk/issues/201)'s second direction: if the range contains `pkg/` changes from a commit whose own release never ran, refuse rather than default to patch. Rejected on two counts.

It fires on the ordinary case. A commit whose own release never ran is precisely what a multi-commit range *is*, and 23 of this repository's 44 ranges are multi-commit. Roughly half of all releases would stop and wait for a human — trading a rare wrong-sized tag for a frequent halt, on a pipeline whose value is that it runs unattended.

And it cannot be answered from git. "Whose own release never ran" is a question about GitHub Actions runs, so the check needs the Actions API inside a script that today reads nothing but the commit graph — untestable offline, and a network dependency on the one path that must not acquire flaky failure modes.

It also fixes nothing: a refusal still leaves the maintainer to re-run the cancelled CI by hand, which is what already happened. Reading the range *produces the right tag*. The half of the second direction worth keeping — making the hazard visible — is kept as the stderr provenance line (Decision 7), which costs nothing and blocks nothing.

### Why not leave the range computation duplicated in both scripts

It was duplicated in spirit already — one script inferred a range, the other inferred a commit — and that is the defect. Two copies of the same rule agree until one is edited.

### Why not newest trailer wins

A commit that says nothing about the bump is not saying "patch"; it is saying nothing. Letting silence overwrite a maintainer's signature is the defect restated with more steps.

### Why not drop the per-commit path scoping and read the range as one unit

Simpler, and it breaks ADR 0007 §2's security property: a trailer in a docs-only merge would ride on an unrelated commit's `pkg/` change. The scoping is what makes the trailer unsmugglable through a PR body, and it only survives commit by commit.

## Deferred

- **Nothing runs the BATS suite.** `scripts/release/*.bats` is executed by no CI lane and no `make` target; the regression tests added here were run locally with `bats-core` v1.11.1. Wiring a lane touches `bazel-ci.yml` and is left to a change that owns that file.
- **`Release-bump: patch` is not vocabulary.** It ranks with "unrecognised" and is therefore harmless, but it is not documented as accepted either. Spelling the vocabulary out — and refusing an unrecognised value loudly — is a separate decision about how strict the trailer should be.
- **The `internal/`-observable minor** still requires the trailer on a `pkg/`-touching commit (see §Consequences).

## References

- <https://github.com/kitsunium/sdk/issues/201> — the incident and the two directions considered
- [git-interpret-trailers](https://git-scm.com/docs/git-interpret-trailers) — trailer syntax
- [`git log --format` `%(trailers:…)`](https://git-scm.com/docs/git-log#Documentation/git-log.txt-emtrailersoptionsem) — `key`, `valueonly`, `separator`
- [docs.github.com — `workflow_run` trigger](https://docs.github.com/en/actions/writing-workflows/choosing-when-your-workflow-runs/events-that-trigger-workflows#workflow_run)
- ADR 0007 §2 — bump semantics (amended here)
- ADR 0009 — the detached release commit, and the `major` refusal
