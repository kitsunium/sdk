<!-- updated: 2026-10-04T04:30:00Z -->
# docs/site/src/styles/

## Purpose

`global.css`, the portal's one stylesheet, imported by
`../layouts/Default.astro`: framework-free (no Tailwind, no UI library),
readable on desktop and mobile, GitHub-dark by default with a light palette.
Components keep their own scoped `<style>`; what lives here is the shell and
what the materialised Markdown renders into.

## Contents

| Section | Styles |
|---|---|
| `:root` | the tokens: `--color-*` (background, borders, text, accents, code, and the `good`/`bad`/`baseline` bench markers), `--font-sans`, `--font-mono`, `--sidebar-w`, `--container-w` |
| App shell, Page body, Sidebar | the sidebar-plus-content grid, the article with its right-hand table of contents, the navigation |
| Markdown body, Home hero, Provenance fact-strip | the rendered READMEs and ADRs, the Home page's head, the build-provenance band |
| Responsive, Light theme palette, Reduced motion, Skip-to-content | the breakpoints; `:root[data-theme="light"]` redefining the tokens; `prefers-reduced-motion`; the keyboard skip link |
| Breadcrumbs, Prev / Next, Edit-this-page, Scrollspy, Theme toggle | the chrome components' shared rules |
| Benchmark tables, GitHub-style alerts, Tabs, API reference, API section (docs/api), Mermaid, Deprecation chips | what the content renders into: `BENCH.md` tables with their green/red markers, `> [!NOTE]` callouts, tab sets (`[data-tabs]`), gomarkdoc's collapsed symbol dump (a release without `docs/api`), `ApiSection.astro`'s symbols, members, codes and platform notes — a symbol's heading highlighted when it is the `:target` —, Mermaid diagrams, obsolete symbols in search |

## Rules

- **The palette is the tokens.** The light theme redefines the `--color-*`
  tokens under `:root[data-theme="light"]`; a rule that writes a literal colour
  — a few translucent `rgba()` tints do — does not follow the theme unless the
  light section restates it.
- **`data-theme` is set before paint** by the inline boot in `Default.astro`;
  the stylesheet only reacts to it.
- **The bench markers keep their meaning**: `--color-good`, `--color-bad` and
  `--color-baseline` are what a reader of a `BENCH.md` table relies on.
- **A component's own look stays in its scoped `<style>`**; a rule lands here
  when the shell or the materialised Markdown needs it.

## Verify

```sh
cd docs/site && npm run build && npm run preview   # check both themes and a narrow window
```
