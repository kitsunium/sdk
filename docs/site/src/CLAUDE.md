<!-- updated: 2026-10-04T04:30:00Z -->
# docs/site/src/

## Purpose

The Astro side of the docs portal: the content collection's schema, the
routes, the layout, the components and the stylesheet that render what
`../scripts/` materialised. Nothing here is prose — every page body comes from
the repository (`docs/site/CLAUDE.md`).

## Contents

| Path | Holds |
|---|---|
| `content.config.ts` | the `docs` collection: a glob loader over `src/content/docs/**/*.md` whose ids keep the on-disk `<release>/<major>/…` path verbatim (the default slugifier strips the dots of `0.1.5`), and a loose schema — `title`, `description`, `updated`, `source`, everything else passed through, since most pages are READMEs without frontmatter |
| `pages/` | the routes: `index.astro` (`/` → the default major's default release), `404.astro` (recovery links from the site default), `rss.xml.js` (every materialised page), `[release]/index.astro` (`/<release>/` → its default major) and `[release]/[major]/[...slug].astro`, the whole versioned tree — title from frontmatter, else the body's first H1, else the last path segment; an id ending in `/index` served at its directory (`pagePath`) |
| `layouts/` | `Default.astro`, the page shell — see `layouts/CLAUDE.md` |
| `components/` | the header, navigation, search and banner components — see `components/CLAUDE.md` |
| `data/` | `features.mjs`, the curated feature catalogue, and the JSON the prebuild writes beside it — see `data/CLAUDE.md` |
| `styles/` | `global.css`, the one stylesheet — see `styles/CLAUDE.md` |
| `content/docs/` | the materialised tree — gitignored, wiped and rewritten by every prebuild; never edited |

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
- **A route reads the collection, never the repository**: what a page shows was
  decided at prebuild.

## Verify

```sh
cd docs/site && npm run build     # prebuild, astro build, pagefind
make serve                        # from the repository root
```
