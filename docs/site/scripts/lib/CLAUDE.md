<!-- updated: 2026-10-04T04:30:00Z -->
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
| `tag-format.mjs` | `TAG_REGEX` (the SDK module's root `vX.Y.Z` tag — ADR 0162 —, the `pkg/vX.Y.Z` tags of the releases before it, and the reserved `pkg/vN/vX.Y.Z` shape), `LOCAL_RELEASE`, `isValidTag`, `parseTag`, `groupByMajor`, `pickLatest`, `dropTombstones` (a `pkg/vX.Y.Z` beside the root tag of its version is the tombstone ADR 0162 cut, no release), `buildVersionsJson` (the two-axis `versions.json`), `localOnlyVersions`, `stitchVersions`, `selectReleases` (the releases `DOCS_RELEASES` names, a default kept per major and for the site), `changelogRefSpec`, `findDefaults` | `sync-versions.mjs`; `Header`, `ReleaseDropdown`, `Sidebar`, `Search`, `PrevNext`, `404.astro` |
| `packages.mjs` | `listPackageDirs` (every directory under `pkg/<major>/` holding a `README.md`, at any depth — `internal`, `testdata` and dot or underscore names never entered), `rewriteReadmeLinks` (the root README's package links turned into portal routes, every other relative link into a GitHub blob link), `rewritePackageDocLinks` (a `BENCH.md`'s or `USES.md`'s relative links for the page they land on: another package's `BENCH.md` is its page's Benchmarks, a `README.md` or directory its page, any other file GitHub) | `sync-versions.mjs` |
| `adr.mjs` | `rewriteAdrLinks` — an ADR's link to another ADR (`0162-….md`, `./…`, `../adr/…`) pointed at its page, `../0162-…/`, and to any other Markdown file of the repository at GitHub; fenced code, absolute links and anchors untouched | `sync-versions.mjs` |
| `api.mjs` | `docs/api` read for the portal. `API_FORMAT` (`sdk.api/v1`, the one it reads), `MODEL_FORMAT`, `SECTION_ORDER`; `readApi` (every module document of a tree's `docs/api`, or null without `schema.json`; another format refused by name); `splitGoID`, `escapePath`, `typeID`, `isPointerMethod` (the go: id grammar of `schema.json`); `splitTopLevel`, `splitTypeParams`, `typeDecl`, `declOf`, `oneLine` (a record's declaration as gofmt starts it, a struct or interface over several lines); `docBlocks`, `inlineSegments`, `synopsis`, `isDeprecated` (doc text as go/doc reads it, as data); `pageOf`, `apiPages` (one page model per package of `pkg/<major>/`: its symbols by section, each form per platform, an alias's owner with its fields and methods under the alias's name), `pageAnchors`, `symbolEntries` (the ⌘K entries of a page); `sourceRef`, `blobBase` (source links at the release's ref) | `sync-versions.mjs`, `gen-symbols.mjs`; `ApiSection`, `ApiDoc`, the page route |
| `page-catalog.mjs` | `RESERVED` (the reserved slugs and their labels), `GROUP_ORDER` (Overview, Packages, Reference), `pagePath` (the path an entry id is served at: `<release>/<major>/index` is `<release>/<major>`, `…/adr/index` is `…/adr`), `buildCatalog`, `flattenCatalog` — the grouped, ordered taxonomy of the content collection; `adr` and `contributors` are built but kept out of the main navigation | `Sidebar`, `Search`, `PrevNext`, `Breadcrumbs`, `404.astro`, the page route, `rss.xml.js` |
| `features.mjs` | `DOMAIN_ORDER`, `DOMAIN_LABEL`, `deriveProvenance` (a feature's date: the commit that added its anchor file, found with `git log --follow` read newest first, because git follows no rename on a reversed walk), `buildFeaturePayload`, `renderCatalogMarkdown`, `uniqueAnchors` | `sync-versions.mjs`, `gen-features.mjs` |
| `base.mjs` | `DEPLOY_BASE` and `withBase(path)` — the project-page base (`/sdk`) a hand-written absolute URL must carry | the components and pages only: it reads `import.meta.env.BASE_URL`, which Vite injects, and never runs under plain `node` |
| `tag-format.test.mjs`, `packages.test.mjs`, `adr.test.mjs`, `api.test.mjs`, `features.test.mjs` | the versioning policy, the tag-vs-ref contract and the release selection; package discovery across family directories, the catalog's handling of those pages and of the `/index` ids the collection gives, and the links of a package's docs; an ADR's links; the go: ids against `docs/api/schema.json`'s vectors, the declarations, the doc text, the page models and ⌘K entries over a two-module fixture, and the repository's own `docs/api` — every `pkg/v1` page has a model, and the entries count the census's alias members; the catalogue logic over a fixture registry | `npm test` |

## Rules

- **`tag-format.mjs` mirrors `scripts/release/lib/tag-format.sh`**: the version
  dropdown parses the tags the release cuts, so the two regexes change in the
  same commit (ADR 0007 §1).
- **A package page sits at its path under `pkg/<major>/`**, family directory
  included (`/<release>/<major>/data/codec/`, ADR 0155); a family directory with
  no `README.md` is no page. Listing only the immediate children published four
  packages once the families existed.
- **`api.mjs` reads one format, `sdk.api/v1`, and refuses any other by name.**
  `docs/api/schema.json` is the format; a change to it changes this reader in
  the same commit, and its go: id vectors are the reader's test too.
- **An anchor is the Go name, and every page renders the anchor its ⌘K entry
  links to**: `Name`, `Type.Method`, and an alias's members under the alias —
  `Alias.Method`, `Alias.Field`. `pageAnchors` and `symbolEntries` read the same
  model, and `../check-api-counts.mjs` derives the anchors again from
  `docs/api`.
- **Doc text stays data**: `docBlocks` and `inlineSegments` return text and
  links, never markup, so no caller has a string to parse as HTML.
- **No I/O beyond what a function is for.** Discovery reads a directory tree,
  `readApi` reads `docs/api`, and provenance asks git; everything else takes its
  inputs as arguments, which is what keeps the suites free of fixtures beyond a
  temporary tree.
- **A feature's date is derived, never typed in**: repoint its anchor in
  `src/data/features.mjs` when it mis-dates one.

## Verify

```sh
cd docs/site && npm test
```
