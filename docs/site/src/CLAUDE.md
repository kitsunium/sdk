<!-- updated: 2026-10-04T04:30:00Z -->
# docs/site/src/

## Purpose

The Astro side of the docs portal: the content collections' schemas, the
routes, the layout, the components and the stylesheet that render what
`../scripts/` materialised. Nothing here is prose — every page body comes from
the repository, a package page's API from `docs/api` (`docs/site/CLAUDE.md`).

## Contents

| Path | Holds |
|---|---|
| `content.config.ts` | three collections, each a glob loader whose ids keep the on-disk `<release>/<major>/…` path verbatim (the default slugifier strips the dots of `0.1.5`): `docs` over `src/content/docs/**/*.md`, with a loose schema — `title`, `description`, `updated`, `source`, `api` (the page has an API section), everything else passed through, since most pages are READMEs without frontmatter; `api` over `src/content/api/**/*.json`, one page model per package page of a release with `docs/api` (`format: 1`, the rest passed through — its shape is `scripts/lib/api.mjs`'s); `bench` over `src/content/bench/**/*.md`, the benchmarks of such a page. A page, its model and its benchmarks share one id |
| `pages/` | the routes: `index.astro` (`/` → the default major's default release), `404.astro` (recovery links from the site default), `rss.xml.js` (every materialised page), `[release]/index.astro` (`/<release>/` → its default major) and `[release]/[major]/[...slug].astro`, the whole versioned tree — title from frontmatter, else the body's first H1, else the last path segment; an id ending in `/index` served at its directory (`pagePath`); a page with `api: true` renders its body, then `ApiSection` from its model, then its benchmarks, and fails the build when the model is missing |
| `layouts/` | `Default.astro`, the page shell — see `layouts/CLAUDE.md` |
| `components/` | the header, navigation, search and banner components — see `components/CLAUDE.md` |
| `data/` | `features.mjs`, the curated feature catalogue, and the JSON the prebuild writes beside it — see `data/CLAUDE.md` |
| `styles/` | `global.css`, the one stylesheet — see `styles/CLAUDE.md` |
| `content/docs/`, `content/api/`, `content/bench/` | the materialised trees — gitignored, wiped and rewritten by every prebuild; never edited |

`pages/` and its subdirectories carry no `CLAUDE.md` on purpose: Astro turns
every `.md` file under `src/pages/` into a route, so one there would be
published as a page — and under `[release]/` it would be a dynamic route with
no `getStaticPaths`, which fails the build. They are described here.

## Rules

- **URLs are `/<release>/<major>/<page>`**: the release is the time axis
  (`local`, a tag's version), the major the API surface (`v1`). The redirects
  are real pages rather than Astro's `redirects` map, so each has an
  `<html lang>` shell Pagefind accepts, and a `data-pagefind-ignore` body.
- **A hand-written absolute URL goes through `withBase`**
  (`../scripts/lib/base.mjs`): the site is served under `/<repo>/`, and Astro
  prefixes only the URLs it controls.
- **A route reads the collections, never the repository**: what a page shows was
  decided at prebuild — its API section included, read from the `api`
  collection rather than from `docs/api`.

## Verify

```sh
cd docs/site && npm run build     # prebuild, astro build, pagefind
make serve                        # from the repository root
```
