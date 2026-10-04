<!-- updated: 2026-10-04T04:30:00Z -->
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
| `prebuild` | `scripts/sync-versions.mjs` | `src/content/docs/<release>/<major>/**` (gitignored), `src/data/versions.json`, `src/data/build-info.json`, the `src/data/features-<release>-<major>.json` banner data (curated in `src/data/features.mjs`; `scripts/gen-features.mjs` regenerates the local release's alone) |
| | `scripts/gen-symbols.mjs` → `tools/genindex` | `public/_search/symbols-<major>.json` (⌘K search index) |
| `build` | `astro build` + `pagefind` | `dist/` + `dist/_pagefind/` |
| `test` | `node --test scripts/lib/*.test.mjs` | tag-format, feature-catalog, package-discovery, page-catalog and ADR-link unit tests |

Run `make docs` / `make serve` from the repo root (they call the above); `make docs-dev` runs `npm run dev`, the same sync then `astro dev`.

## Layout

| Path | Holds |
|---|---|
| `scripts/` | the build scripts of the table above |
| `scripts/lib/` | shared modules — `base.mjs` (`withBase`), `tag-format.mjs`, `features.mjs` (the curated-feature logic), `packages.mjs` (which directories of `pkg/<major>/` become package pages, and how links reach them), `page-catalog.mjs` (the page taxonomy `Sidebar.astro`, `Search.astro` and `PrevNext.astro` share), `adr.mjs` (an ADR's relative links on the portal) — and their `*.test.mjs` |
| `src/data/` | `features.mjs`, the curated feature catalogue; the generated JSON lands beside it |
| `src/pages/` | `index.astro`, `404.astro`, `rss.xml.js`, and `[release]/index.astro` + `[release]/[major]/[...slug].astro` rendering the synced content |
| `src/components/`, `src/layouts/`, `src/styles/` | the Astro components, `Default.astro`, `global.css` |

Each of these directories has its own `CLAUDE.md` but `src/pages/`: Astro
publishes every `.md` file under `src/pages/` as a route — under `[release]/`
a dynamic one without `getStaticPaths`, which fails the build — so the routes
are described in `src/CLAUDE.md` instead.

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

The version dropdown is driven by `versions.json`, built from the releases
`gh release list` returns, or — without `gh` — from `git tag -l 'v*' 'pkg/v*'`:
the SDK module's root `vX.Y.Z` tags (ADR 0162) and the `pkg/vX.Y.Z` tags of the
releases before it (ADR 0017), one list on the `v1` axis; the vendor modules'
tags are no release of their own, and neither is the tombstone `pkg/v0.18.0`
beside `v0.18.0` (ADR 0162), which `dropTombstones` leaves out. Each tagged release is snapshotted via
`git worktree`. `scripts/lib/tag-format.mjs` is the JS mirror of
`scripts/release/lib/tag-format.sh` (same TAG_REGEX — edit together, ADR 0007
§1). Tag-format + version-defaulting logic is unit-tested (`npm test`).

## Conventions

- Never hand-edit `src/content/docs/**` — it is regenerated each build. Edit the
  upstream source (package doc comments, ADRs, README) instead.
- Hand-written absolute internal URLs go through `withBase()`.
- Generated markdown uses relative links (`./data/codec/`) — base-agnostic, no
  help needed. A relative link the source wrote for GitHub is rewritten for the
  page it lands on: an ADR's link to another ADR points at its page
  (`lib/adr.mjs`), a `BENCH.md`'s `../BENCH.md` at the family page's
  Benchmarks (`lib/packages.mjs`), any other file of the repository at GitHub.
- A page's id keeps its file path (`src/content.config.ts`), so the landing
  page is `<release>/<major>/index` and the ADR index `…/adr/index`; the route
  serves each at its directory, and the catalog, the breadcrumbs and the feed
  read ids through `pagePath`.
- A package page sits at the package's path under `pkg/<major>/`, family
  directory included (`/<release>/<major>/data/codec/` — ADR 0155): every
  directory below `pkg/<major>/` holding a `README.md` gets one, at any depth,
  `internal/` and `testdata/` excepted. A family directory that is not itself a
  package (`pkg/v1/data`) gets no page, and its breadcrumb is text, not a link.
  Listing only `pkg/<major>`'s immediate children published four packages once
  the families existed.

## Do NOT

- Commit `dist/`, `src/content/docs/local/**`, `versions.json`, or
  `build-info.json` expecting them to be authoritative — they are build outputs.
- Add a hand-written `href="/x"` without `withBase("/x")` — it 404s under the
  project-page base.
