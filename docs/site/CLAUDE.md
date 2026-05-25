# docs/site/

## Purpose

The kitsunium/sdk documentation portal — an Astro static site published to
GitHub Pages. Content is **generated, not hand-authored**: the prebuild step
materialises every page from the real source repo (package READMEs, ADRs,
CLAUDE.md sections, the root README) and from `git log`, so the site can never
drift from the code.

## Build pipeline

`npm run build` → `prebuild` (sync) then `astro build` + `pagefind`:

| Step | Script | Emits |
|---|---|---|
| `prebuild` | `scripts/sync-versions.mjs` | `src/content/docs/<release>/<major>/**` (gitignored), `src/data/versions.json`, `src/data/build-info.json`, the `whats-new-*.json` banner |
| | `scripts/gen-symbols.mjs` → `tools/genindex` | `public/_search/symbols-<major>.json` (⌘K search index) |
| `build` | `astro build` + `pagefind` | `dist/` + `dist/_pagefind/` |
| `test` | `node --test scripts/**/*.test.mjs` | versioning-logic unit tests |

Run `make docs` / `make serve` from the repo root (they call the above).

## Deployment + base path

Deployed by `.github/workflows/docs-deploy.yml` to GitHub Pages. It is a
**project page** served under `/<repo>/` (e.g. `/sdk/`), so:

- `astro.config.mjs` sets `site` = the org origin and `base` = `/<repo>` (both
  derived from `build-info.repoUrl`; override with `DOCS_SITE_URL` / `DOCS_BASE`
  for a custom domain).
- Astro auto-prefixes assets it controls, but **hand-written absolute URLs do
  not get the base** — prefix them with the helper in `scripts/lib/base.mjs`
  (`DEPLOY_BASE` / `withBase`). `Default.astro` strips the base from
  `currentPath` so the `/<release>/<major>/` parsing in the nav components works
  unchanged; canonical/OG URLs use the full (base-included) path.

A clean way to catch a base regression: build, then crawl `dist/` resolving
every link under `/<repo>/` — any link outside it is a missing-base bug.

## Versioning (ADR 0007)

The version dropdown is driven by `versions.json`, built from
`git tag -l 'pkg/v*/v*'`. Each tagged release is snapshotted via
`git worktree`. `scripts/lib/tag-format.mjs` is the JS mirror of
`scripts/release/lib/tag-format.sh` (same TAG_REGEX — edit together, ADR 0007
§1). Tag-format + version-defaulting logic is unit-tested (`npm test`).

## Conventions

- Never hand-edit `src/content/docs/**` — it is regenerated each build. Edit the
  upstream source (package doc comments, ADRs, README) instead.
- Hand-written absolute internal URLs go through `withBase()`.
- Generated markdown uses relative links (`./codec/`) — base-agnostic, no help
  needed.

## Do NOT

- Commit `dist/`, `src/content/docs/local/**`, `versions.json`, or
  `build-info.json` expecting them to be authoritative — they are build outputs.
- Add a hand-written `href="/x"` without `withBase("/x")` — it 404s under the
  project-page base.
