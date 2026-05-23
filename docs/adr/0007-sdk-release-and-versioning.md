# ADR 0007 — SDK release workflow and versioning policy

**Status**: Accepted
**Date**: 2026-05-22
**Deciders**: kitsunium maintainers
**Supersedes**: —
**Amends**: ADR 0001 §Negative (release ritual partially addressed)
**Related**: ADR 0004 (Bazel as build SOT), ADR 0005 (dotted-quad error codes)

## Context

The SDK ships as a multi-module Go workspace (root + `internal/{kernel,core,service}` + `pkg/v1`, glued by `go.work`). The only consumer-facing module is `pkg/v1`; `internal/*` is blocked from external import by Go's `internal/` rule and Bazel visibility. Before this ADR there were no tagged releases, no automated bump policy, no documented mapping between an SDK consumer's `go get @vX.Y.Z` and the documentation rendered on the docs site.

Two questions drove the design:

1. How should code changes propagate to version increments without manual bookkeeping that always drifts?
2. How should the docs site expose every released major while keeping a single source of truth for which tag is "current"?

## Decision

### 1. Tag format (load-bearing)

Every release tag follows:

    pkg/<major>/v<MAJOR>.<MINOR>.<PATCH>[-<prerelease>]?

The `pkg/<major>/` prefix is required by Go's module path resolver when releasing a module that lives in a subdirectory (`go.dev/ref/mod#vcs-version`). Single source of truth: `scripts/release/lib/tag-format.sh` (re-exported to JS as `docs/site/scripts/lib/tag-format.mjs`).

### 2. Bump semantics

| Trigger | Bump |
|---|---|
| Change under `pkg/<major>/**` | patch on `<major>` |
| Change under `internal/**` and Bazel rdeps reach `//pkg/<major>/...` | patch on `<major>` |
| Behavioral change in `internal/**` observable through `pkg/<major>` | **minor**, not patch — semver §6 requires it |
| `Release-bump: minor` trailer on the merge commit AND that commit touches `pkg/<major>/` | minor on `<major>` |
| New major (`v2`) | mkdir `pkg/v2/`, first tag `pkg/v2/v2.0.0` |

Patch is auto. Minor is gated by a trailer that an attacker cannot smuggle through a PR body — the trailer is read from the merge commit only, and it is honored only for the majors whose paths that commit touches. Major bumps are manual (the module path must change per Go's semantic-import-versioning rule).

### 3. Impact analysis

`scripts/release/compute-bumps.sh` reads `git diff --name-only <last-tag>..HEAD`, classifies each path, and for every `internal/<X>` touched runs `bazel query 'rdeps(//pkg/<major>/..., //internal/<X>/...)'`. A non-empty result triggers a patch bump on `<major>`. Bazel is invoked once per touched internal package, never per file.

Bazel's role here is purely **read-only graph query** — release orchestration deliberately lives outside the Bazel build graph (see §Scope below).

### 4. Release pipeline

`.github/workflows/sdk-release.yml` is triggered by `workflow_run` on `SDK CI (Bazel)` success, NOT by `push: main` (eliminates double Bazel runs and prevents tagging a failing build). Manual override via `workflow_dispatch.inputs.force_bumps`.

`cut-tags.sh`:

1. Reads majors on stdin.
2. Looks up the latest valid tag per major via `latest_tag_for`.
3. Computes the next tag via `next_patch` / `next_minor` (trailer-scoped).
4. Strips `replace` lines from a temp copy of `pkg/<major>/go.mod`, then verifies `GOWORK=off go mod download all` resolves cleanly. Refuses to tag if it doesn't.
5. Re-reads the latest tag immediately before push and aborts on drift (race protection).
6. `git push --atomic origin <tag>`.

### 5. Docs versioning

`docs/site/scripts/sync-versions.mjs` is invoked as `npm run prebuild`. It:

1. Calls `gh release list --json tagName,publishedAt,isDraft,isPrerelease`.
2. Filters tags through the canonical regex (`tag-format.mjs`) — drops drafts, pre-releases, and shell-injection bait.
3. Picks the latest per major.
4. For each (major, latest tag): `git worktree add /tmp/wt-<major> <tag>`, copies `pkg/<major>/**/README.md` + `**/BENCH.md` + `docs/adr/*.md` into `docs/site/src/content/docs/<release>/<major>/`, then `git worktree remove --force` in a `finally`. The on-disk layout is `<release>/<major>` (release is the time axis — snapshot in git; major is the API surface — interface that evolves), matching the live URLs `/<release>/<major>/<page>` the Astro catch-all renders.
5. Writes `docs/site/src/data/versions.json` with `{major, latest, default, eol, publishedAt}`. Newest non-EOL major is `default: true`.

The sidebar's `<VersionDropdown />` reads `versions.json` at build time. The dropdown labels are `v1`, `v2`, … with `(latest: X.Y.Z)` suffix. EOL entries render with an `(EOL)` flag and stay served indefinitely (deprecation ritual = flip `eol: true` in the JSON; no 404).

### 6. Reserved routes (top-level page collision guard)

`getting-started`, `philosophy`, `architecture`, `packages`, `adr`, `benchmarks`, `verification`, `index` are reserved page slugs under `/<release>/<major>/`. A future major must not be named after any of them (they're not valid semver majors anyway, but the regex would allow weirdness like `pkg/getting-started/v1.0.0`). The tag-format regex anchors `^pkg/v[0-9]+/…` and rejects them. Reserved RELEASE names (`local` today; future: tag-derived `v0.1.0` etc.) are non-colliding with major names because majors always start with `v\d+` and the canonical catch-all distinguishes the two by position, not pattern.

### 7. Logo asset

`docs/site/public/brand/kitsunium.jpg` is committed from `https://avatars.githubusercontent.com/u/183549249?v=4`. Provenance recorded here so any future rotation is auditable:

| Source | SHA-256 | Date |
|---|---|---|
| avatars.githubusercontent.com/u/183549249?v=4 (JPEG, 460×460) | `1b70c39b5525bcd1acfc60579f4a15bdeec2f2a0ae4a1f700edbbc3ec53e3c48` | 2026-05-22 |

Distribution: org-owned asset, redistributed under the repo's license. The pre-commit guard fails on byte drift.

### 8. Rollback

For a bad auto-bump:

    git push --delete origin <bad-tag>
    gh release delete <bad-tag> --yes

The workflow's concurrency group is `sdk-release-${{ github.ref }}` with `cancel-in-progress: false` — a mid-flight tag push is never cancelled by a follow-up commit.

A monthly rollback rehearsal is run via `workflow_dispatch` against a throwaway branch tagged `pkg/v1/v0.0.0-rehearsal` — both create and delete steps must succeed under the workflow's permissions.

### 9. Major version migration

When `pkg/v1` graduates and a breaking change lands:

1. `mkdir pkg/v2 && cp -r pkg/v1/* pkg/v2/` then edit.
2. Update the module path in `pkg/v2/go.mod` to `github.com/kitsunium/sdk/pkg/v2`.
3. Add `./pkg/v2` to `go.work`.
4. First tag: `pkg/v2/v2.0.0`. `versions.json` auto-picks up.

`pkg/v1` keeps receiving patch bumps until EOL.

## Consequences

- **Positive**: tagged releases are now byte-traceable to a commit + a bump rule; the docs always match a real `go get` of the corresponding tag; minor bumps require a maintainer-signed signal.
- **Positive**: ADR 0004 (Bazel as build SOT) is unchanged — Bazel queries the graph, never owns the release.
- **Negative**: an internal change that is behaviorally observable demands a maintainer to add the `Release-bump: minor` trailer — CI cannot detect this automatically. Mitigated by review discipline.
- **Negative**: `go.work` must be disabled (`GOWORK=off`) during the tagging step so the published `go.mod` graph resolves through the proxy.
- **Negative**: docs build time scales with N majors. Acceptable while N ≤ 5; a `last-N-only` policy is the planned mitigation if it grows further.

## Scope (clarifies ADR 0004)

ADR 0004 makes Bazel the **build** system of record. Release orchestration is deliberately outside the Bazel build graph: there is no `sh_binary` wrapping `cut-tags.sh`, no `genrule` cutting tags. The release pipeline reads from Bazel (via `bazel query rdeps`) and writes to git+GitHub. This split keeps the build hermetic and the release pipeline observable; bridging the two would buy nothing.

## Alternatives considered

- **goreleaser**: rejected. Designed for cross-compiled binaries + archives + package managers; pure-Go SDKs publish via `go get` automatically after a tag. Earns its keep only when shipping a CLI.
- **Date-based tags (`v2026.05.22-<sha>`)**: rejected. Breaks semver, breaks `go get @v1.x.y`, breaks every downstream tool.
- **Single root-level tag `vX.Y.Z` for all modules**: rejected. Go's module resolver requires the `pkg/<major>/` prefix when the module lives in a subdirectory.
- **`<meta http-equiv="refresh">` for the docs root redirect**: rejected. Astro 5's `redirects:` config emits proper 301s, no flash, better SEO.
- **Patch bump on behavioral changes**: rejected. Violates semver §6 ("no observable behavior change"). The minor-trailer ritual is the price of correctness.

## Why-not blanket-bump every major on every `internal/*` change

Tempting (no Bazel needed). Rejected because the v1 → v2 transition phase will inevitably leave one of the two majors untouched by most internal refactors — bumping both would create noise and break consumer assumptions about what a patch tag implies.

## Deferred

Items deliberately deferred to a follow-up ADR / future PR:

- Pre-release channel (rc.x sub-dropdown).
- Sigstore tag-signing.
- `scripts/release/` rename to `scripts/ci/release/`.
- `proxy.golang.org` ingestion throttling (2s sleep between pushes).
- Zero-time pseudo-version preflight (`v0.0.0-00010101000000-000000000000` sentinel rejection).
- Apple-touch-icon / favicon-512 variants.
- End-to-end synthetic-repo smoke test (currently covered by BATS unit tests).

## References

- [go.dev/ref/mod#vcs-version](https://go.dev/ref/mod#vcs-version) — sub-directory tag prefix rule
- [go.dev/doc/modules/major-version](https://go.dev/doc/modules/major-version) — semantic import versioning
- [semver.org](https://semver.org/) §6 — patch semantics
- [docs.astro.build/en/guides/routing#redirects](https://docs.astro.build/en/guides/routing#redirects)
- [docs.github.com — workflow_run trigger](https://docs.github.com/en/actions/writing-workflows/choosing-when-your-workflow-runs/events-that-trigger-workflows#workflow_run)
- ADR 0001 §Negative — release ritual placeholder (this ADR fulfills it)
- ADR 0004 — Bazel as build SOT (this ADR scopes around it)
