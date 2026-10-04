<!-- updated: 2026-10-04T04:30:00Z -->
# docs/site/src/components/

## Purpose

The portal's chrome, as Astro components: the header band and its version
controls, the navigation, the search, the edit link, the banner — and a
package page's API section. Each derives what it shows from the content
collections, `versions.json` and `build-info.json` — no page list, repository
name, version or symbol is written into a component. The layout that assembles
them is `../layouts/Default.astro`.

## Contents

| Component | Renders | Placed by |
|---|---|---|
| `Header.astro` | the top band: build provenance (date, branch) on the left, the release pill (the commit SHA for `local`, else the page's own release) on the right | `Default.astro` |
| `ApiDropdown.astro` | the API major selector; hides itself when only one major exists | `Header` |
| `ReleaseDropdown.astro` | the release selector for the current major — the page's OWN release coordinate, never a global "latest"; the SHA only beside `local`, the one snapshot that is HEAD | `Header` |
| `Sidebar.astro` | the navigation, grouped and ordered by `page-catalog.mjs`; footer links — For contributors, the repository and pkg.go.dev (both derived from `build-info.repoUrl`), the RSS feed | `Default.astro` |
| `Brand.astro` | the logo and "SDK" block at the top of the sidebar | `Sidebar` |
| `Search.astro` | ⌘K / Ctrl+K search, two sources behind one input, both fetched on first use and under the deploy base: Pagefind's full text over the built site, and the symbol index of the page's own release, `symbols-<release>-<major>.json` — gen-symbols.mjs's, from `docs/api`, schema 2 — searched with MiniSearch; none for a release `versions.json` does not flag `api`. Each result is built element by element, its text in `textContent`; following one closes the modal. Quick links from the catalog while the input is empty. Re-bound on every `astro:page-load`, one keyboard listener acting on the page shown | `Sidebar` |
| `ApiSection.astro` | a package page's API from its page model (`scripts/lib/api.mjs` `apiPages`): constants, variables, functions and types, each symbol a heading anchored at its Go name with its kind, its source file at the release's ref, its forms per platform, its code and what it re-exports; a type's fields and methods; an alias's owner — the declaration of the type it names, or its pkg.go.dev link outside the SDK — with that type's fields and methods anchored under the alias (`Locker.TryAcquire`) | `[release]/[major]/[...slug].astro`, on a page whose frontmatter says `api: true` |
| `ApiDoc.astro` | one doc comment of the API section: `docBlocks` paragraphs, `# ` headings, code and lists, `inlineSegments` links to an anchor of the page or an http(s) URL — every string an Astro expression | `ApiSection` |
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
- **Text from the code is rendered as text**: a doc comment, a signature, a
  value goes into an Astro expression (escaped) or a `textContent` — never
  `set:html` or `innerHTML`. Structure comes from `docBlocks` and
  `inlineSegments`, which return data.
- **An API anchor is the ⌘K entry's**: `ApiSection` gives every symbol and
  member the id `symbolEntries` links to, and `../../scripts/check-api-counts.mjs`
  fails the build check when one is missing.

## Attention point

A client script's URLs carry the base too: `Search.astro` reads
`import.meta.env.BASE_URL` for Pagefind, the symbol index and every result,
and strips it from `window.location.pathname` before reading the page's
release. It once fetched `/_pagefind/…` and `/_search/…` bare, so under `/sdk/`
neither search loaded; its rows, built outside the component, matched none of
its scoped styles either — the rows' selectors reach them through `:global()`.

## Verify

```sh
cd docs/site && npm run build && npm run preview   # or `make serve` from the root
```
