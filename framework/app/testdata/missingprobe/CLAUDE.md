<!-- updated: 2026-10-17T23:00:00Z -->
# framework/app/testdata/missingprobe — a store declared, its subsystem not imported

A test fixture, not a package a product imports: `cliprobe`'s service and
default command — not fail-safe, so that its refusal reaches its standard
error — with a store declared, on `framework/app` alone, without
`framework/app/store`. Since ADR 0179's step 10 the facade imports no
registration package, so this is a real product a hand-written program can
be: `initgraph_external_test.go`'s
`TestAProductThatDeclaresAStoreWithoutItsImportIsRefused` builds and runs it,
and requires the start's refusal (§6): exit status 78, nothing on standard
output, and on standard error the store (`line/store/entries`) and the import
to add (`add import _ "github.com/kitsunium/sdk/framework/app/store"`); and
nothing of the stores' code linked.

## Contents

| File | Holds |
|---|---|
| `main.go` | the product: `Line`, its `entries` store and `render` command, `main` |
| `README.md` | written by hand: a `main` under `testdata/` is no package docs/api records, so tools/genindex writes no README for it (ADR 0167) |

## Rules

- It imports `framework/app` and the standard library only: adding `framework/app/store` would make it start, and the test fail.
- Its README is written by hand and says what its package comment says: an edit of one is an edit of the other.

## Verify

```sh
cd framework && GOWORK=off go test ./kit/ -run 'WithoutItsImport' -v
```
