<!-- updated: 2026-10-17T22:00:00Z -->
# framework/kit/testdata/storeprobe — the smallest product, with the stores imported

A test fixture, not a package a product imports: `coreprobe` with
`framework/kit/store` blank-imported. `initgraph_external_test.go`'s
`TestAProductThatImportsTheStoresLinksThem` builds it and finds linked the
store subsystem's engines (`framework/internal/kit/storekit`) and the SDK's
document store and sql under them, and every function of kit's own store
code the test lists (`kitStoreCode`, step 3b) — the half of ADR 0179 §7 that keeps the
other from passing by testing nothing —, and none of the queue subsystem's
brokers nor of the SDK's queue beyond its init: the stores do not bring the
queues (step 4, Q4).

## Contents

| File | Holds |
|---|---|
| `main.go` | the product: `Line`, its `render` command, `main`, and the blank import of `framework/kit/store` |
| `README.md` | written by hand: a `main` under `testdata/` is no package docs/api records, so tools/genindex writes no README for it (ADR 0167) |

## Rules

- It imports `framework/internal/kit`, `framework/kit/store` and the standard library only.
- Its README is written by hand and says what its package comment says: an edit of one is an edit of the other.

## Verify

```sh
cd framework && GOWORK=off go test ./kit/ -run 'ImportsTheStores' -v
```
