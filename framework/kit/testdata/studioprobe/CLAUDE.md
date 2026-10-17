<!-- updated: 2026-10-17T23:00:00Z -->
# framework/kit/testdata/studioprobe — the smallest product, with the Studio imported

A test fixture, not a package a product imports: `coreprobe` with
`framework/kit/studio` blank-imported — and no registration package of ADR
0179: the Studio imports none. `initgraph_external_test.go`'s
`TestAProductThatImportsTheStudioLinksItsLanguages` builds it and finds linked
kit's catalogues after English — `framework/internal/kit/studiokit`'s
`openWords` and the SDK's i18n under it, beyond its inits — and French in the
binary (a sentence of `studiokit/locales/fr.json`), beside kit's English
table, and nothing of the store, queue, workflow, mail and jobs subsystems'
engines — the half of ADR 0179 §7 that keeps the other from passing by
testing nothing, for step 8. Every other probe holds neither the SDK's i18n
nor the French catalogue (`linksNoWords`).

## Contents

| File | Holds |
|---|---|
| `main.go` | the product: `Line`, its `render` command, `main`, and the blank import of `framework/kit/studio` |
| `README.md` | written by hand: a `main` under `testdata/` is no package docs/api records, so tools/genindex writes no README for it (ADR 0167) |

## Rules

- It imports `framework/internal/kit`, `framework/kit/studio` and the standard library only.
- Its README is written by hand and says what its package comment says: an edit of one is an edit of the other.

## Verify

```sh
cd framework && GOWORK=off go test ./kit/ -run 'ImportsTheStudio' -v
```
