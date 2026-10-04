<!-- updated: 2026-10-04T00:32:31Z -->
# docs/site/scripts/lib/

## Purpose

The logic the docs build and the Astro components share, kept out of the
scripts so it can be unit-tested without git, the GitHub API or a build: pure
functions over strings, lists and directory trees. `npm test` runs
`node --test scripts/lib/*.test.mjs` — the only lane of this directory's
tests. The build that uses them is `../CLAUDE.md`.

## Contents

| File | Exports | Used by |
|---|---|---|
| `tag-format.mjs` | `TAG_REGEX` (the SDK module's root `vX.Y.Z` tag — ADR 0162 —, the `pkg/vX.Y.Z` tags of the releases before it, and the reserved `pkg/vN/vX.Y.Z` shape), `LOCAL_RELEASE`, `isValidTag`, `parseTag`, `groupByMajor`, `pickLatest`, `dropTombstones` (a `pkg/vX.Y.Z` beside the root tag of its version is the tombstone ADR 0162 cut, no release), `buildVersionsJson` (the two-axis `versions.json`), `localOnlyVersions`, `stitchVersions`, `changelogRefSpec`, `findDefaults` | `sync-versions.mjs`; `Header`, `ReleaseDropdown`, `Sidebar`, `Search`, `PrevNext`, `404.astro` |
| `packages.mjs` | `listPackageDirs` (every directory under `pkg/<major>/` holding a `README.md`, at any depth — `internal`, `testdata` and dot or underscore names never entered), `rewriteReadmeLinks` (the root README's package links turned into portal routes, every other relative link into a GitHub blob link) | `sync-versions.mjs` |
| `page-catalog.mjs` | `RESERVED` (the reserved slugs and their labels), `GROUP_ORDER` (Overview, Packages, Reference), `buildCatalog`, `flattenCatalog` — the grouped, ordered taxonomy of the content collection; `adr` and `contributors` are built but kept out of the main navigation | `Sidebar`, `Search`, `PrevNext`, `Breadcrumbs`, `404.astro` |
| `features.mjs` | `DOMAIN_ORDER`, `DOMAIN_LABEL`, `deriveProvenance` (a feature's date: the commit that added its anchor file, found with `git log --follow` read newest first, because git follows no rename on a reversed walk), `buildFeaturePayload`, `renderCatalogMarkdown`, `uniqueAnchors` | `sync-versions.mjs`, `gen-features.mjs` |
| `base.mjs` | `DEPLOY_BASE` and `withBase(path)` — the project-page base (`/sdk`) a hand-written absolute URL must carry | the components and pages only: it reads `import.meta.env.BASE_URL`, which Vite injects, and never runs under plain `node` |
| `tag-format.test.mjs`, `packages.test.mjs`, `features.test.mjs` | the versioning policy and the tag-vs-ref contract; package discovery across family directories and the catalog's handling of those pages; the catalogue logic over a fixture registry | `npm test` |

## Rules

- **`tag-format.mjs` mirrors `scripts/release/lib/tag-format.sh`**: the version
  dropdown parses the tags the release cuts, so the two regexes change in the
  same commit (ADR 0007 §1).
- **A package page sits at its path under `pkg/<major>/`**, family directory
  included (`/<release>/<major>/data/codec/`, ADR 0155); a family directory with
  no `README.md` is no page. Listing only the immediate children published four
  packages once the families existed.
- **No I/O beyond what a function is for.** Discovery reads a directory tree and
  provenance asks git; everything else takes its inputs as arguments, which is
  what keeps the suites free of fixtures beyond a temporary tree.
- **A feature's date is derived, never typed in**: repoint its anchor in
  `src/data/features.mjs` when it mis-dates one.

## Verify

```sh
cd docs/site && npm test
```
