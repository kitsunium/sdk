<!-- updated: 2026-10-04T10:00:00Z -->
# docs/site/

## Purpose

The kitsunium/sdk documentation portal — an Astro static site published to
GitHub Pages. Content is **generated, not hand-authored**: the prebuild step
materialises every page from the real source repo (package READMEs, ADRs,
CLAUDE.md sections, the root README), from `docs/api` — the exported API
`tools/genindex` reads from the code — and from `git log`, so the site can
never drift from the code.

## Build pipeline

`npm run build` → `prebuild` (sync) then the content sync, `astro build` + `pagefind`:

| Step | Script | Emits |
|---|---|---|
| `prebuild` | `scripts/sync-versions.mjs` | `src/content/docs/<release>/<major>/**` (gitignored); for a release whose tree has `docs/api`, one API page model per package page in `src/content/api/<release>/<major>/<path>.json` and, for a package with a `BENCH.md`, its benchmarks in `src/content/bench/…` (both gitignored); `src/data/versions.json` (a release with `docs/api` flagged `api`), `src/data/build-info.json`, the `src/data/features-<release>-<major>.json` banner data (curated in `src/data/features.mjs`; `scripts/gen-features.mjs` regenerates the local release's alone) |
| | `scripts/gen-symbols.mjs` | `public/_search/symbols-<release>-<major>.json`, the ⌘K symbol index of each release flagged `api`, from its page models — Node only, no go command |
| `build` | `scripts/sync-content.mjs`, then `astro build` + `pagefind` | `node_modules/.astro/data-store.json` (the content store, written before the build reads it), then `dist/` + `dist/_pagefind/` |
| `test` | `node --test scripts/lib/*.test.mjs` | tag-format, feature-catalog, package-discovery, page-catalog, ADR-link and docs/api-reader unit tests |
| `check` | `scripts/check-api-counts.mjs`, `scripts/check-links.mjs` | nothing: fails unless the ⌘K index and the API sections equal `docs/api`, and every link of `dist/` resolves (§Checks) |

Run `make docs` / `make serve` from the repo root (they call the above); `make docs-dev` runs `npm run dev`, the same sync then `astro dev`. `make docs-check` is the gate: `npm ci`, `npm test`, a build of the working tree alone, `npm run check`.

**The content store is written before `astro build` reads it, and must be.**
The prebuild wipes it (`node_modules/.astro/`), and a fresh store is written
while the content syncs, by a debounced write. When the sync ends with that
write still in flight, `astro build` (5.18) reads the store before it lands —
the file absent, so every collection empty — and renders 80 pages out of
9,500 with exit status 0: a full build here did. `astro sync` is no cure, the
CLI exiting the process as soon as its command resolves. `sync-content.mjs`
runs the same sync through Astro's JavaScript API and lets Node exit once the
write has landed; `astro build` then loads the complete store and re-syncs
only what changed, in seconds.

`DOCS_RELEASES=local` (or `local,0.18.0`, …) materialises the releases it names
and no other — the dropdowns list exactly those, and asking for `local` alone
lists no release at all, so no `gh` and no network. A build of the working tree
takes about 25 s; every release, about 16 minutes on an M1 Pro.

## The API from docs/api

`docs/api/<module>.json` records every exported symbol of every module with its
signature as its file spells it, its doc text, its cells and its file, read
with go/types on the twelve platforms (`make api`, held byte for byte by
`make api-check`). The portal reads it through `scripts/lib/api.mjs` alone:

- **Each package page of `pkg/<major>/` gets an API section**
  (`src/components/ApiSection.astro`), in place of gomarkdoc's symbol dump: every
  constant, variable, function and type of the package, each form a symbol has
  per platform (`concur/group.Unlimited` is `2147483647` on linux/386 and
  linux/arm), its code (dotted quad, reason, public text) and what it
  re-exports — and **an alias carries the declaration, the fields and the
  methods of the type it names**, anchored under the alias's name
  (`/local/v1/app/lock/#Locker.TryAcquire`). go/doc does not follow an alias, so
  gomarkdoc and the go/doc index this replaced never showed them; the READMEs
  have since ADR 0167, written by genindex from the same docs/api.
- **The ⌘K symbol index is the same records**: every exported symbol of the
  package's page, and every method an alias reaches at its owner, each linking
  to the record its page renders.
- **Doc text is rendered as text.** `docBlocks` splits a comment the way go/doc
  reads one (paragraphs, `# ` headings, code, lists), `inlineSegments` cuts it
  into text and links (a doc link the page anchors, an http(s) URL), and the
  components put each piece in an Astro expression or a `textContent` — no HTML
  is built from a string, and the search results are DOM nodes, not
  `innerHTML`.
- **A release whose tree has no `docs/api`** — every tag cut before it existed —
  keeps its README pages as they were, gomarkdoc's reference included, and has
  no symbol index: Pagefind's full text finds its pages.

ADR 0163 §12 records this reading of `docs/api`, the old releases' pages and
the two checks; `docs/api` itself is held to the code and to `design/` by
`make api-check`, so what the portal shows is what the code declares and the
design says.

At `79e3ba7d`, the go/doc index held 1,967 entries for `pkg/v1`; the index from
`docs/api` holds 2,777: the same 1,967 symbols, and the 810 methods the 480
aliases of `pkg/v1` reach at their owners. The API sections anchor 3,685
records — those, and the 898 fields of the aliased structs.

## Checks

| Script | Fails unless |
|---|---|
| `scripts/check-api-counts.mjs` | for one release and major (default `local`, `v1`): the ⌘K entries declared by a package of `pkg/<major>/` are exactly the distinct exported symbols `docs/api` records there; the entries reached through an alias are exactly, per alias, the methods `docs/api` records for its `owner`; each entry links to its record's anchor; and, with `dist/` built, every package page carries an API section and an element for every symbol, alias method and alias field. It reads `docs/api` with code of its own — it imports nothing from `lib/api.mjs` |
| `scripts/check-links.mjs` | every `href` and `src` of every page of `dist/`, and every ⌘K entry, resolves the way a browser would on the project page: under the deploy base, to a page or file that exists, at a fragment the target page declares (`id` or `<a name>`). A fragment missing on the README page of a release without `docs/api` is counted and printed, not failed: the README of a published tag cannot be edited |

CI's `docs-site` job (`bazel-ci.yml`) runs `make docs-check` on every pull
request; `docs-deploy.yml` runs `npm run check` over every release it builds,
before it deploys.

## Layout

| Path | Holds |
|---|---|
| `scripts/` | the build scripts of the table above, and the two checks |
| `scripts/lib/` | shared modules — `base.mjs` (`withBase`), `tag-format.mjs`, `features.mjs` (the curated-feature logic), `packages.mjs` (which directories of `pkg/<major>/` become package pages, and how links reach them), `page-catalog.mjs` (the page taxonomy `Sidebar.astro`, `Search.astro` and `PrevNext.astro` share), `api.mjs` (docs/api read for the portal), `adr.mjs` (an ADR's relative links on the portal) — and their `*.test.mjs` |
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
  (`DEPLOY_BASE` / `withBase`), or in a client script with
  `import.meta.env.BASE_URL`, as `Search.astro` does for Pagefind, the symbol
  index and every result. `Default.astro` strips the base from `currentPath`
  so the `/<release>/<major>/` parsing in the nav components works unchanged;
  canonical/OG URLs use the full (base-included) path.

`scripts/check-links.mjs` is the base regression check: it crawls `dist/` and
fails on any link outside `/<repo>/`.

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

- Never hand-edit `src/content/docs/**`, `src/content/api/**` or
  `src/content/bench/**` — they are regenerated each build. Edit the upstream
  source (package doc comments, then `make api`; ADRs; README) instead.
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

- Commit `dist/`, `src/content/docs/local/**`, `src/content/api/`,
  `src/content/bench/`, `versions.json`, or `build-info.json` expecting them to
  be authoritative — they are build outputs.
- Add a hand-written `href="/x"` without `withBase("/x")` — it 404s under the
  project-page base.
- Render doc text through `set:html` or `innerHTML`: it is data from comments,
  and `docBlocks` / `inlineSegments` already give the structure a page needs.
