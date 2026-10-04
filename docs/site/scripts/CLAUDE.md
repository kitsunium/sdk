<!-- updated: 2026-10-04T04:30:00Z -->
# docs/site/scripts/

## Purpose

The Node build steps of the docs portal: everything the Astro site renders is
materialised here, at `npm run prebuild` (and `npm run dev`), from the real
repository — package READMEs, ADRs, two sections of the root `CLAUDE.md`,
`git log` — so the site carries no prose of its own. Plain ES modules run by
`node`, no build step of their own. The portal as a whole — the pipeline
table, the base path, the versioning — is `docs/site/CLAUDE.md`.

## Contents

| File | What it does | Writes |
|---|---|---|
| `sync-versions.mjs` | lists the releases (`gh release list`, else `git tag -l`), stitches them with the majors on disk (`lib/tag-format.mjs`), wipes the content tree and Astro's caches, and materialises every (release, major): the root `README.md` as Home (its package links rewritten to portal routes), a page per package under `pkg/<major>/` (`lib/packages.mjs` — README, then the package's `USES.md` and `BENCH.md` when present, their relative links rewritten for the page), the ADRs (their `CLAUDE.md` skipped, their links to each other pointed at their pages — `lib/adr.mjs`), `concepts` from the root `CLAUDE.md`'s **Architecture at a glance** section, `contributors` with its **Verification** section, `getting-started` from `docs/getting-started.md`, the changelog from `git log`, and the feature catalogue (`lib/features.mjs`). A tagged release is read from a `git worktree` of its tag | `src/content/docs/<release>/<major>/**`, `src/data/versions.json`, `src/data/build-info.json`, `src/data/features-<release>-<major>.json` |
| `gen-symbols.mjs` | for each major in `versions.json`, runs `tools/genindex` with `GOWORK=off` (it is outside `go.work`) and a source-link prefix derived from `build-info.json` | `public/_search/symbols-<major>.json`, served at `/_search/` |
| `gen-features.mjs` | regenerates the local release's feature-banner data alone, for every `pkg/v<N>/` major (`v1` when `pkg/` is absent) — a development and verification convenience; the prebuild produces the same data for every release | `src/data/features-<local>-<major>.json` |
| `lib/` | the shared, unit-tested modules — see `lib/CLAUDE.md` | — |

## Rules

- **The site renders, it does not author.** A page's text is fixed where it
  comes from — a doc comment, an ADR, a README, the root `CLAUDE.md` — never in
  the materialised tree, which is wiped on every run.
- **Two root `CLAUDE.md` sections are published**, by their exact H2 titles:
  `Architecture at a glance` (the Concepts page) and `Verification` (on the
  contributors page). Renaming either drops its page without an error.
- **Every page records its `source`** in its frontmatter, the repository-relative
  file `EditLink.astro` links to.
- **The wipe is total**: the content tree, `.astro/` and
  `node_modules/.astro/` (the content store behind Astro's "Duplicate id"
  warnings) are removed before anything is written.

## Attention points

- Comments here still say `/workspace/CLAUDE.md` (the devcontainer path ADR
  0153 removed) and, in `gen-symbols.mjs`, that the index lands in `src/data/`;
  the code writes `public/_search/` and reads the root `CLAUDE.md` of the tree
  it materialises — the checkout, or a release's worktree.
- `sync-versions.mjs` lists the releases from `gh release list`, or from
  `git tag -l 'v*' 'pkg/v*'` without `gh`: the SDK module's root tags since ADR
  0162 and the `pkg/vX.Y.Z` history before them, one list on the `v1` axis;
  a `pkg/vX.Y.Z` at a root tag's version is that release's tombstone, dropped.

## Verify

```sh
cd docs/site && npm ci && npm run prebuild   # needs git and go; gh when available
cd docs/site && npm test                     # the lib/ unit tests
make docs                                    # the whole build, from the repository root
```
