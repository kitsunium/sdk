<!-- updated: 2026-10-04T04:30:00Z -->
# docs/site/src/data/

## Purpose

The data the portal's components read. One file is written by hand —
`features.mjs`, the curated catalogue of public capabilities behind the Home
page's "What's new" banner and the Features page; everything else here is
written by the prebuild (`../../scripts/sync-versions.mjs`) and is gitignored.

## Contents

| File | Origin | Holds |
|---|---|---|
| `features.mjs` | tracked, hand-written | the default export: one entry per consumer-facing capability — `id` (a stable kebab-case slug), `domain` (one of `DOMAIN_ORDER` in `../../scripts/lib/features.mjs`), `title`, `blurb` (one sentence), `anchor` (one file under `pkg/v1/**` whose first commit dates the feature), optional `links` (`adr`, `pr`) and `breaking` |
| `versions.json` | generated, ignored | the two-axis release list: per major, its releases (`local` first), which is the default, which is end-of-life, and `api` on a release whose tree has `docs/api` — its package pages carry API sections and it has a ⌘K symbol index. Only the releases `DOCS_RELEASES` names, when it is set |
| `build-info.json` | generated, ignored | the build's commit, date, branch, dirty flag and `repoUrl`, from which the layout derives the project name, the site URL and the footer links |
| `features-<release>-<major>.json` | generated, ignored | the banner and catalogue payload for one release and major, each feature dated from its anchor |

## Rules

- **A date is never typed in.** A feature's "added" date is the commit that
  first added its `anchor`, found by `git log --follow` so a moved file keeps
  its date (the tree-by-family moves renamed every anchor — ADR 0155). When an
  anchor mis-dates a feature, repoint it at a more precise FILE (never a
  directory); provenance throws on an anchor that resolves to no commit.
- **An entry is a public capability a consumer can use**, written for a
  consumer, grouped by domain — not a commit, not an internal change.
- **A new domain is added to `DOMAIN_ORDER` and `DOMAIN_LABEL`** in
  `../../scripts/lib/features.mjs`; a feature whose domain is not listed sorts
  last, and the unit test flags it.
- **Never commit a generated file here**: the prebuild rewrites them all, and a
  committed `versions.json` would claim releases the build no longer sees.

## Verify

```sh
cd docs/site && node scripts/gen-features.mjs   # regenerates the local release's payload
cd docs/site && npm test                        # the catalogue logic
```
