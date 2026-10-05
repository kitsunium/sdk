<!-- updated: 2026-10-05T00:00:00Z -->
# framework/kit/storetest — the conformance suite of kit's stores

What a store does on every backend (the platform's ADR 0004): run by
`framework/internal/kit`'s tests on memory, files and the fake database
(`storetest_test.go`), and by each engine module's tests on its engine
(`connectors/{sqlite,postgres,mysql}`). It keeps the test double honest: a
product tested in memory sees what its database does.

## Contents

| File | Holds |
|---|---|
| `storetest.go` | `Run` and `BackendConfig` — where the stores run, the options that place them —, `CodeRolledBack` (`0.4.4.1`, the code of `errRolled`, the error a case returns to roll its transaction back), and the cases: write modes, one insert of a key winning, atomic updates, a unique index naming itself, key order, keys as bytes, documents as written, transactions and savepoints, a publish and a write's hooks held until the commit, revisions (kept, pruned, stamped, diffed, restored, rolled back) |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- A case declares a service of its own, named after the case, so the cases of one run can share one database: their tables never meet.
- A case uses the facade (`framework/kit`) only: it sees the stores as a product does.
- A new case is added to `cases`, and runs on every backend of every caller.
- An error the suite returns itself is typed (rule 2: no `errors.New`, which `make guard` fails on) in this package's own range, `0.4.4.*` — layer 4, PP 4, recorded in `codeRangeOwners`; the package is in `//:audit_sources`.

## Verify

```sh
cd framework && GOWORK=off go test -race -run TestStoresConform ./internal/kit/ && (cd connectors/sqlite && GOWORK=off go test -race -run TestStoresConform ./...)
```
