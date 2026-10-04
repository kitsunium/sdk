<!-- updated: 2026-10-04T04:30:00Z -->
# docs/site/scripts/

## Purpose

The Node build steps of the docs portal: everything the Astro site renders is
materialised here, at `npm run prebuild` (and `npm run dev`), from the real
repository — package READMEs, ADRs, two sections of the root `CLAUDE.md`,
`docs/api`, `git log` — so the site carries no prose of its own; and the two
checks that hold the built site to the code. Plain ES modules run by `node`,
no build step of their own, no go command. The portal as a whole — the
pipeline table, the base path, the versioning — is `docs/site/CLAUDE.md`.

## Contents

| File | What it does | Writes |
|---|---|---|
| `sync-versions.mjs` | lists the releases (`gh release list`, else `git tag -l`; none when `DOCS_RELEASES` names `local` alone), stitches them with the majors on disk and keeps those `DOCS_RELEASES` names (`lib/tag-format.mjs`), wipes the content trees and Astro's caches, and materialises every (release, major): the root `README.md` as Home (its package links rewritten to portal routes), a page per package under `pkg/<major>/` (`lib/packages.mjs` — README, then the package's `USES.md` and `BENCH.md` when present, their relative links rewritten for the page), the ADRs (their `CLAUDE.md` skipped, their links to each other pointed at their pages — `lib/adr.mjs`), `concepts` from the root `CLAUDE.md`'s **Architecture at a glance** section, `contributors` with its **Verification** section, `getting-started` from `docs/getting-started.md`, the changelog from `git log`, and the feature catalogue (`lib/features.mjs`). When the release's tree has `docs/api`, each package page gets the page model of its API (`lib/api.mjs`), marked `api: true` in its frontmatter, gomarkdoc's symbol dump is left out of its body and its benchmarks go to the bench collection; the release is flagged `api` in `versions.json`. A tagged release is read from a `git worktree` of its tag | `src/content/docs/<release>/<major>/**`, `src/content/api/<release>/<major>/<path>.json`, `src/content/bench/<release>/<major>/<path>.md`, `src/data/versions.json`, `src/data/build-info.json`, `src/data/features-<release>-<major>.json` |
| `gen-symbols.mjs` | for each release `versions.json` flags `api`, turns its page models into the ⌘K entries (`lib/api.mjs` `symbolEntries`): every exported symbol of the package pages, and every method an alias reaches at its owner, each with the url of the record its page renders and a source link at the release's ref (its tag, else the branch or commit `build-info.json` names). Wipes `public/_search/` first; fails on a release flagged `api` with no model | `public/_search/symbols-<release>-<major>.json` (schema 2, `source: "docs/api"`), served at `<base>/_search/` |
| `sync-content.mjs` | the first step of `npm run build`: Astro's content sync, through its JavaScript API, in a process Node ends only once nothing is pending — so once the content store's last write has landed; fails when the store is still missing. `astro build` would otherwise read a store whose write was still in flight and render every collection empty, exit status 0 (`docs/site/CLAUDE.md` §Build pipeline) | `node_modules/.astro/data-store.json` |
| `gen-features.mjs` | regenerates the local release's feature-banner data alone, for every `pkg/v<N>/` major (`v1` when `pkg/` is absent) — a development and verification convenience; the prebuild produces the same data for every release | `src/data/features-<local>-<major>.json` |
| `check-api-counts.mjs` | `npm run check`'s first half: reads `docs/api` with code of its own and fails unless the ⌘K index of a release (`--release`, default `local`; `--major`, default `v1`) holds exactly the distinct exported symbols of `pkg/<major>/` and, per alias, the methods of its owner, each at its record's url, and — with `dist/` built — every package page anchors every symbol, alias method and alias field. Prints the counts | nothing |
| `check-links.mjs` | `npm run check`'s second half: crawls `dist/` and resolves every `href`, `src` and ⌘K url under the deploy base (`--base`, else as `astro.config.mjs` derives it), fragments included, except a fragment on the README page of a release without `docs/api`, which it counts. `--show N` lists N links per kind of failure | nothing |
| `lib/` | the shared, unit-tested modules — see `lib/CLAUDE.md` | — |

## Rules

- **The site renders, it does not author.** A page's text is fixed where it
  comes from — a doc comment (through `docs/api` or a README), an ADR, a
  README, the root `CLAUDE.md` — never in the materialised tree, which is wiped
  on every run.
- **Two root `CLAUDE.md` sections are published**, by their exact H2 titles:
  `Architecture at a glance` (the Concepts page) and `Verification` (on the
  contributors page). Renaming either drops its page without an error.
- **Every page records its `source`** in its frontmatter, the repository-relative
  file `EditLink.astro` links to.
- **The wipe is total**: `sync-versions.mjs` removes the three content trees,
  `.astro/` and `node_modules/.astro/` (the content store behind Astro's
  "Duplicate id" warnings) before it writes anything, and `gen-symbols.mjs`
  removes `public/_search/` — a stale index would be copied into `dist/`.
- **A check never shares code with what it checks.** `check-api-counts.mjs`
  reads `docs/api` itself rather than through `lib/api.mjs`: a projection that
  dropped a symbol from both the pages and the index would otherwise pass.

## Attention points

- Comments in `sync-versions.mjs` still say `/workspace/CLAUDE.md` (the
  devcontainer path ADR 0153 removed); the code reads the root `CLAUDE.md` of
  the tree it materialises — the checkout, or a release's worktree.
- `sync-versions.mjs` lists the releases from `gh release list`, or from
  `git tag -l 'v*' 'pkg/v*'` without `gh`: the SDK module's root tags since ADR
  0162 and the `pkg/vX.Y.Z` history before them, one list on the `v1` axis;
  a `pkg/vX.Y.Z` at a root tag's version is that release's tombstone, dropped.
- Every tag up to `pkg/v0.17.0` predates `docs/api`: those releases have README
  pages and no symbol index. The first release cut with this tree is the first
  whose own pages carry API sections.

## Verify

```sh
cd docs/site && npm ci && npm test                              # the lib/ unit tests
cd docs/site && DOCS_RELEASES=local npm run build && npm run check   # the working tree, checked
make docs-check                                                  # the same, as CI's docs-site job runs it
make docs                                                        # every release, from the repository root
```
