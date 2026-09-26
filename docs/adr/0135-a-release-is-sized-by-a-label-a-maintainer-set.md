# ADR 0135 — A release is sized by a label a maintainer set, never by text a merge composed

- **Status**: Accepted; implemented in `scripts/release/lib/release-size.sh`, `scripts/release/cut-tags.sh`, `scripts/release/check-pr-size.sh`, `.github/workflows/sdk-release.yml` and `.github/workflows/release-size.yml`.
- **Date**: 2026-09-26
- **Deciders**: kitsunium maintainers
- **Amends**: [ADR 0007](0007-sdk-release-and-versioning.md) §2 (row 4 and the paragraph under the table — the channel that sizes a minor), [ADR 0085](0085-both-halves-of-a-release-read-the-same-range.md) (what is read over the range: a label instead of a trailer; the range itself is unchanged), [ADR 0089](0089-a-file-that-cannot-cut-a-release-cannot-size-one.md) (§Deferred, first item — closed without the repository setting it proposed)
- **Related**: [ADR 0009](0009-pkg-public-module-resolvability.md) (the detached release commit), [ADR 0088](0088-a-suite-nothing-runs-is-not-a-test-suite.md) (the suites run in CI)

## Context

Two scripts decide a release. `compute-bumps.sh` answers **whether** (ADR 0007 §2 rows 1–2, reachability through Bazel `rdeps`); `cut-tags.sh` answers **how big**. Since ADR 0085 the second read a `Release-bump:` trailer from every first-parent commit of the range, and since ADR 0089 only from commits that could themselves cut a release. ADR 0007 §2 justified the channel with one sentence: *"Minor is gated by a trailer that an attacker cannot smuggle through a PR body — the trailer is read from the merge commit only."*

That guarantee rested on the merge commit being the maintainer's text, and in this repository it is not. The repository squash-merges — sixteen of sixteen first-parent commits measured in [#224](https://github.com/kitsunium/sdk/issues/224), and every one since — with `squash_merge_commit_message: COMMIT_MESSAGES`, so GitHub composes the message the release reads from the **branch commits**, which is contributor text. One fact, three measured failures:

1. **Contributor text buried the maintainer's request.** `%(trailers:…)` reads the last paragraph only, and a folded branch commit after the trailer made it invisible. [`3f681412`](https://github.com/kitsunium/sdk/commit/3f681412) ([#185](https://github.com/kitsunium/sdk/pull/185), trailer at line 99 of 126) shipped as `pkg/v0.1.35`, and [`e435af8c`](https://github.com/kitsunium/sdk/commit/e435af8c) ([#207](https://github.com/kitsunium/sdk/pull/207), line 106 of 139) as `pkg/v0.3.4` — two minors published as patches, in silence ([#217](https://github.com/kitsunium/sdk/issues/217)).
2. **The fix for that stopped the release instead — and the only way past was a hand-run script.** ADR 0089's refusal (a `Release-bump:` the message carries and git did not parse) turned the silence into a stop, which is right, and then had no exit. [`ce9323c`](https://github.com/kitsunium/sdk/commit/ce9323c) ([#248](https://github.com/kitsunium/sdk/pull/248)) carried one branch commit's `Release-bump: minor` at line 135 of a 194-line squash message; [run 36197439900](https://github.com/kitsunium/sdk/actions/runs/36197439900) exited 65 with *"carries a 'Release-bump:' OUTSIDE the trailer block"*; and `pkg/v0.6.0` was then cut outside the pipeline, by `cut-tags.sh --range` from a workstation (tagger `kodflow <contact@making.codes>`, `+0200`, where the pipeline tags as `kitsunium-bot` in UTC), with a range chosen to step over the refused commit. The same happened to `pkg/v0.4.0`, refused twice by [run 34896970885](https://github.com/kitsunium/sdk/actions/runs/34896970885) and [run 34897728008](https://github.com/kitsunium/sdk/actions/runs/34897728008) and then cut by hand ([#217](https://github.com/kitsunium/sdk/issues/217), comment of 2026-09-15). A range chosen by hand to avoid one commit's request also avoids every other request in it, so the escape hatch was a way to lose a size deliberately.
3. **Contributor text set the size.** A squash message whose last paragraph is a contributor's `Release-bump: minor` parses exactly like a maintainer's, and ADR 0089's refusal does not fire because git parsed as many trailers as the message carries. Reproduced in [#224](https://github.com/kitsunium/sdk/issues/224) with the script's own incantations: `minor` honoured.

The trailer was also merely hard to write correctly. One malformed neighbour (`Refs #127`, no colon) removes the whole paragraph from the trailer block; a `---` line changes what `git interpret-trailers` returns but not what `%(trailers:…)` returns ([ADR 0089](0089-a-file-that-cannot-cut-a-release-cannot-size-one.md) §Deferred); `separator=,` once turned `mi` and `nor` into `minor` ([ADR 0085](0085-both-halves-of-a-release-read-the-same-range.md)). And a trailer written in the merge dialog is invisible to anything that runs before the merge: [#249](https://github.com/kitsunium/sdk/pull/249)'s `Release-bump: minor` is in [`679aad1`](https://github.com/kitsunium/sdk/commit/679aad1) and in none of its five branch commits.

### What a pull request carries that a message does not

A **label**. GitHub lets only an account with triage or write access on the repository apply one; the author of a pull request from outside cannot label their own. It is structured data — a name, not a paragraph — so none of the parsing traps above apply to it. It is visible on the pull request before the merge and readable through the API after it (`GET /repos/{owner}/{repo}/commits/{sha}/pulls` answers, for a commit on the default branch, the merged pull request that introduced it — measured on this repository: [`ce9323c`](https://github.com/kitsunium/sdk/commit/ce9323c) → #248, [`679aad1`](https://github.com/kitsunium/sdk/commit/679aad1) → #249, [`e435af8c`](https://github.com/kitsunium/sdk/commit/e435af8c) → #207, and an unknown commit → HTTP 422, an error rather than an empty list).

## Decision

**The size of a release is the largest `release:*` label on the merged pull requests behind the release range's first-parent commits that could cut a release. A message only asks.**

1. **Labels.** `release:patch`, `release:minor`, `release:major` (compared case-insensitively, as GitHub compares label names). For every first-parent commit of the range (ADR 0085) that touches a path able to cut a release (ADR 0089, `lib/release-scope.sh`), `cut-tags.sh` reads the labels of the merged pull request(s) that introduced it. The largest size wins; no label anywhere is a patch, the default ADR 0007 §2 always had. A commit that cannot cut a release is not looked up at all.
2. **A label decides, in both directions.** `release:patch` on a pull request whose commits ask for a minor is how a maintainer declines a contributor's request without rewriting anybody's commits.
3. **A message asks, and an unanswered request stops the release.** A column-0 `Release-bump: <size>` line **anywhere** in such a commit's message, asking for more than a patch, with no label to decide it, is refused (exit 65) before anything is tagged. Publishing the patch would be [#217](https://github.com/kitsunium/sdk/issues/217) again; honouring the text would be [#224](https://github.com/kitsunium/sdk/issues/224). Where the line sits no longer matters — a request buried by a folded commit is still a request, which is what makes the #207 shape cut the minor it asked for once it is labelled.
4. **Two different release labels, or a `release:` label that names no size, are refused**, because either answer would be a guess, and a typo read as "no label" is a minor published as a patch.
5. **A lookup that fails is refused**, never read as "no label". A missing `gh` likewise. An absence of measurement is not an absence of result — the rule [#226](https://github.com/kitsunium/sdk/issues/226) and [#227](https://github.com/kitsunium/sdk/issues/227) established for `compute-bumps.sh`.
6. **The way out is a maintainer action inside the pipeline, never a hand-run script.** A refusal names the pull request and both remedies: label it and re-run the failed job (labels are read live), or dispatch SDK Release with the new `bump` input (`patch`, `minor`, `major`), which passes `cut-tags.sh --bump` and sizes the whole range without asking GitHub anything — so an API outage cannot block the way out of an API outage. `workflow_dispatch` requires write access, like a label.
7. **The same question is asked before the merge.** `check-pr-size.sh`, run by `.github/workflows/release-size.yml` on every push and every label change of a pull request, applies the same rule (`lib/release-size.sh`, one copy) to every branch commit — the text the squash will fold in — and fails with the label that settles it. It cannot see text typed in the merge dialog; the release job still refuses that after the merge.
8. **WHETHER to release is unchanged.** ADR 0007 §2 rows 1–2 stand: a change under `pkg/` releases, and so does an `internal/` change whose `rdeps` reach `//pkg/...`. [#226](https://github.com/kitsunium/sdk/issues/226) asked whether such a change *should* publish; it should, because a behavioural fix confined to `internal/` reaches a consumer only through a release, and since [#243](https://github.com/kitsunium/sdk/pull/243) every run names the rule that decided it (`compute-bumps.sh --explain`, the release summary). This ADR changes only how big.

ADR 0007 §2 row 4 now reads: *a `release:minor` label on the merged pull request behind a first-parent commit in the range that can cut a release → minor*; and its closing paragraph's guarantee is restated as: *the size is set by an account with triage or write access, through a label or a dispatch, and never read from text a merge composed.*

## Consequences / Semantics

- **No repository setting changes.** `squash_merge_commit_message` stays `COMMIT_MESSAGES`: the descriptive squash bodies this repository relies on survive, and none of their text sizes anything. The #224 vector is closed by the channel, not by constraining every merge.
- **The release lane reads the API.** `sdk-release.yml` grants `pull-requests: read`. One call per first-parent commit that can cut a release — usually one to three; ADR 0085's largest measured range, 213 commits, stays far inside the token's rate limit.
- **A direct push has no pull request.** Its request, if it makes one, is refused like an unlabelled one, and `bump` settles it. This repository does not push to `main` directly.
- **The labels have to exist.** A label that does not exist cannot be applied, so until a maintainer creates the three, every size above a patch goes through `bump`.
- **Where the size came from is in the run.** `cut-tags.sh` writes `size <x> — label release:<x> on #<n> (<sha>)`, adds `not HEAD` when the sized merge is not the checked-out commit, and the release summary quotes it; a refused run's summary quotes the refusal and the remedies.
- **The record of sizes that were wrong** — immutable, because the module proxy already serves every one of these versions and a moved tag breaks `go.sum` verification for whoever fetched it ([#217](https://github.com/kitsunium/sdk/issues/217)):

  | tag | what happened |
  |---|---|
  | `pkg/v0.1.35` | [`3f681412`](https://github.com/kitsunium/sdk/commit/3f681412) asked for a minor; its trailer was buried; shipped as a patch |
  | `pkg/v0.2.0` | minted ~22 h later for other content — the number the lost minor would have taken |
  | `pkg/v0.3.4` | [`e435af8c`](https://github.com/kitsunium/sdk/commit/e435af8c) asked for a minor; buried; shipped as a patch |
  | `pkg/v0.4.0` | refused twice by the pipeline, then cut by hand, sized by two documentation merges ([#230](https://github.com/kitsunium/sdk/pull/230), [#231](https://github.com/kitsunium/sdk/pull/231)); the number is right for what it published (the new `pkg/v1/clock`) and was reached for the wrong reason; it has no GitHub Release |
  | `pkg/v0.6.0` | refused by the pipeline ([#248](https://github.com/kitsunium/sdk/pull/248)'s buried request), then cut by hand with `--range` |

## Breaking changes

None for consumers: tag shape, module paths, the patch default and the lockstep chain are unchanged. For maintainers, one: **a `Release-bump:` line no longer sizes a release.** Size with `gh pr edit <n> --add-label release:minor`; a line asking for more than a patch with no label now stops the release where it used to cut the minor. `pkg/CLAUDE.md` §Sizing a release is rewritten accordingly.

## Alternatives considered

### Why not `squash_merge_commit_message: BLANK`

It makes the maintainer compose every squash message, which does make the trailer theirs — and it is a repository setting, applied to every merge whether it releases or not, invisible to review and to CI. It keeps every parsing trap in place (the last-paragraph rule, the malformed neighbour, the merge-dialog edit), and it still leaves nothing but the maintainer's memory between a forgotten trailer and a silent patch. A label keeps the rich squash bodies and removes the parser.

### Why not `PR_TITLE`

A trailer cannot live in a title, so the vector closes — by deleting the channel. The size would have to move somewhere else anyway, and the descriptive squash bodies this repository relies on would go with it.

### Why not the pull request body's last paragraph

The body is written and edited by the pull request's author, up to the moment of the merge. For a pull request from outside, that is contributor text again — the exact property the change exists to remove. A label is the only piece of pull request metadata whose writers are the repository's own.

### Why not keep the trailer and add only the dispatch input

It fixes the recovery half and leaves [#224](https://github.com/kitsunium/sdk/issues/224) open: a contributor's well-placed trailer would still size the release, and a buried one would still stop it — now with a better exit, but still a stop on text nobody with the authority wrote.

### Why not warn instead of refusing an unlabelled request

A warning is read after the release it describes, and a published version cannot be taken back: the proxy serves it forever. Warning is exactly how `pkg/v0.1.35` and `pkg/v0.3.4` shipped.

### Why not take the larger of the label and the text

The text is contributor-writable, so the larger of the two is whatever the contributor wrote. That is [#224](https://github.com/kitsunium/sdk/issues/224) with an extra step.

### Why not label automatically from the text

An automation that turns a `Release-bump:` line into a label turns contributor text into the maintainer's channel. The check fails instead and a human applies the label.

## Deferred

- **Making `Release size` a required check** is a repository setting (the legacy branch protection requires `bazel`, the ruleset `post-commit`). Unrequired, it still turns red on the pull request that would stop the release.
- **Creating the three labels** is a write to the repository's metadata, left to a maintainer: `gh label create release:patch`, `release:minor`, `release:major`.
- **The public record of [#217](https://github.com/kitsunium/sdk/issues/217)'s releases.** The table above is the in-repository record; amending the GitHub Release notes of `pkg/v0.2.0` (and creating the missing one for `pkg/v0.4.0`) is an external edit, left to a maintainer.
- **Text typed in the merge dialog** is invisible before the merge. Only the release job sees it, and it refuses; making the dialog's text visible earlier would need the merge to go through a bot.

## References

- [ADR 0007](0007-sdk-release-and-versioning.md) §2 — bump semantics (row 4 and its guarantee amended here)
- [ADR 0085](0085-both-halves-of-a-release-read-the-same-range.md) — both halves read the same range (unchanged; the label is read over it)
- [ADR 0089](0089-a-file-that-cannot-cut-a-release-cannot-size-one.md) — one notion of a file that counts (unchanged; it scopes the label)
- `scripts/release/lib/release-size.sh` — the rule, once: `text_ask`, `label_size`, `decide_size`, `pr_labels_of_commit`
- `scripts/release/cut-tags.sh` — `range_size`, `--bump`
- `scripts/release/check-pr-size.sh`, `.github/workflows/release-size.yml` — the same rule before the merge
- `scripts/release/test-cut-tags.bats`, `scripts/release/test-check-pr-size.bats`, `scripts/release/test-helpers.bash` — the suites, through a `gh` stub
- Issues [#224](https://github.com/kitsunium/sdk/issues/224) (the squash message sizes the release), [#217](https://github.com/kitsunium/sdk/issues/217) (two requests lost), [#218](https://github.com/kitsunium/sdk/issues/218) (ADR 0007 §2 contradicted itself), [#226](https://github.com/kitsunium/sdk/issues/226) (whether an `internal/`-only change publishes)
