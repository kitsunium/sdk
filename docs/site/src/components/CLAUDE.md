<!-- updated: 2026-10-04T04:30:00Z -->
# docs/site/src/components/

## Purpose

The portal's chrome, as Astro components: the header band and its version
controls, the navigation, the search, the edit link, the banner. Each derives
what it shows from the content collection, `versions.json` and `build-info.json`
— no page list, repository name or version is written into a component. The
layout that assembles them is `../layouts/Default.astro`.

## Contents

| Component | Renders | Placed by |
|---|---|---|
| `Header.astro` | the top band: build provenance (date, branch) on the left, the release pill (the commit SHA for `local`, else the page's own release) on the right | `Default.astro` |
| `ApiDropdown.astro` | the API major selector; hides itself when only one major exists | `Header` |
| `ReleaseDropdown.astro` | the release selector for the current major — the page's OWN release coordinate, never a global "latest"; the SHA only beside `local`, the one snapshot that is HEAD | `Header` |
| `Sidebar.astro` | the navigation, grouped and ordered by `page-catalog.mjs`; footer links — For contributors, the repository and pkg.go.dev (both derived from `build-info.repoUrl`), the RSS feed | `Default.astro` |
| `Brand.astro` | the logo and "SDK" block at the top of the sidebar | `Sidebar` |
| `Search.astro` | ⌘K / Ctrl+K search, two sources behind one input, both fetched on first use: Pagefind's full text over the built site, and the `tools/genindex` symbol index (`symbols-<major>.json`) searched with MiniSearch; quick links from the catalog while the input is empty | `Sidebar` |
| `ThemeToggle.astro` | dark (default) / light, kept in `localStorage`; the boot script that applies it before paint is in `Default.astro`'s `<head>` | `Sidebar` |
| `Breadcrumbs.astro` | the in-document path only (the slugs after `<major>`), labelled from the catalog; an intermediate crumb links only to a page that exists | `Default.astro` |
| `PrevNext.astro` | the previous and next page in the catalog's reading order; nothing for a page outside it | `Default.astro` |
| `EditLink.astro` | "Edit on GitHub" to the page's frontmatter `source`, at the branch tip or else the commit; nothing when either is unknown | `Default.astro` |
| `WhatsNew.astro` | the Home page's banner: the most recently added public capabilities from `src/data/features-<release>-<major>.json`; nothing when the file is absent. It is handed an absolute path without the base, which it adds | `[release]/[major]/[...slug].astro`, on Home only — the page whose id is `<release>/<major>/index` |
| `Tabs.astro` | a WAI-ARIA tab set over named slots | no page imports it today; `Default.astro`'s `initTabs` binds any `[data-tabs]` markup |

## Rules

- **Every hand-written absolute URL goes through `withBase`**
  (`../../scripts/lib/base.mjs`); a bare `href="/x"` 404s under the project-page
  base.
- **The catalog is the one taxonomy**: `Sidebar`, `Search`, `PrevNext` and
  `Breadcrumbs` read `../../scripts/lib/page-catalog.mjs`, so a new page appears
  in all four from the next build without touching a component.
- **The two dropdowns are one combobox pattern**, activated by the controller in
  `Default.astro` on every `[data-combobox]` — no browser `<select>`.
- **URLs are `/<release>/<major>/…`**: a component parses that order (release
  first), against the path `Default.astro` hands it with the base stripped.

## Attention point

`Search.astro`'s client script fetches `/_pagefind/pagefind-ui.{js,css}` and
`/_search/symbols-<major>.json` as bare absolute paths, and `activeMajor` reads
the major from `window.location.pathname` without stripping the base — so under
the project-page base (`/sdk/`) both lookups read as misses (the major falls
back to `v1`). Read from the code, not verified against the deployed site; the
component's server half does use `DEPLOY_BASE`.

## Verify

```sh
cd docs/site && npm run build && npm run preview   # or `make serve` from the root
```
