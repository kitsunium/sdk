<!-- updated: 2026-10-03T13:06:04Z -->
# docs/site/src/layouts/

## Purpose

`Default.astro`, the one page shell every route renders through (`404.astro`
and the versioned `[release]/[major]/[...slug].astro`; the redirect pages
carry their own minimal shell). It assembles the components of
`../components/` and owns the client-side behaviour they share.

## Contents

| File | Holds |
|---|---|
| `Default.astro` | the `<head>`: the title and project name derived from `build-info.repoUrl` (the layout writes no repository name of its own), the canonical and Open Graph URLs built from the full path, base included, the RSS link, the inline theme boot that reads `localStorage` before the body paints; the body: `Sidebar`, `Header`, `Breadcrumbs`, `EditLink` and `PrevNext` around the page, then the footer; Astro's `ClientRouter`; and the shared scripts, re-bound on every `astro:page-load` — the combobox controller for every `[data-combobox]`, the tab controller for every `[data-tabs]`, and Mermaid, loaded on the first `mermaid` code block with `securityLevel: "strict"` |

## Rules

- **Two paths, one each for its use.** `fullPath` keeps the deploy base
  (`/sdk`) for the canonical and Open Graph URLs; `currentPath` strips it and is
  what the components parse as `/<release>/<major>/…` — a component that builds
  an `href` from it adds the base back with `withBase`.
- **A shared behaviour is bound here once**, idempotently (a `dataset` flag per
  root), so a component that renders several times, or a client-side
  navigation, never binds it twice.
- **The theme boot stays inline in `<head>`**: it must run before the body
  paints, or the page flashes in the wrong theme.
- **Nothing project-specific is hard-coded here**: the name, the site URL and
  the links come from `build-info.json`, which the prebuild writes.

## Verify

```sh
cd docs/site && npm run build && npm run preview
```
