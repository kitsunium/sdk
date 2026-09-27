# pkg/v1/docstore/

## Purpose

Public facade over `internal/service/docstore` (ADR 0110): typed, keyed JSON
documents with unique and multi-valued secondary indexes, in memory or
persisted through a `vfs.FullFS` — every write durable before it returns, and
a write's cost independent of how many documents the store holds — and the same
contract over SQL, in two tables of a PostgreSQL, MySQL or SQLite database,
every call on the transaction its context carries (ADR 0139).

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Open[T](cfg, indexes...)` | func | a `*Store[T]`, loaded from `cfg.FS` when set |
| `Unique[T](name, key)` / `Index[T](name, keys)` | func | index declarations |
| `Store[T]` | type alias | `Get`, `List`, `Filter`, `Entries`, `Lookup`, `Find`, `Stats`; `Put`, `Insert`, `Replace`, `Update`, `Delete`; `OnWrite`, `OnDelete`; `Fold`, `Close` |
| `Config[T]` | type alias | `Key` (required), `FS` + `Path` (both or neither), `FoldAt` |
| `IndexSpec[T]` | type alias | `Keys`, `Name`, `Unique` |
| `Entry` | type alias | `= svcdocstore.EntryValue` — `Key`, `JSON` (the caller's copy) |
| `Stats` | type alias | `= svcdocstore.StatsValue` — `Documents`, `Pending`, `Folds`, `FoldError` |
| `DefaultFoldAt` | const | 1024 |
| `OpenSQL[T](cfg, indexes...)` | func | a `*SQLStore[T]` over `cfg.Transactor`; sends no statement |
| `SQLStore[T]` | type alias | `Get`, `List`, `Filter`, `Entries`, `Count`, `Lookup`, `Find`; `Put`, `Insert`, `Replace`, `Update`, `Delete`; `OnWrite`, `OnDelete`; `Reindex` — every call takes a context |
| `SQLConfig[T]` | type alias | `Key`, `Transactor`, `Dialect`, `Table` (required), `IndexKey` |
| `SQLMigration(dialect, table, version)` | func | the two tables as one idempotent `sql.Migration` |
| `MaxSQLTableLen` / `MaxSQLKeyLen` / `MaxSQLIndexNameLen` | const | 58 / 1024 / 64 bytes |
| `Code*` | const | `0.3.80.1`–`0.3.80.17` |
| `DocumentNotFound` … `WriteUnconfirmed`, `StatementFailed`, `KeyTooLong` | var | the seventeen sentinels |

All types are aliases onto the service: there is no port, and the values are
the engines' (ADR 0074, ADR 0139 §D2). The SQL signatures name `pkg/v1/sql`'s
aliases (`sql.Dialect`, `sql.Migration`), so a consumer's documentation never
shows an internal package.

## Why-this-shape

- **The indexes are `Open`'s arguments, not a `Config` field**, so a store is
  declared the way a framework declares one — a key, then its indexes — and the
  configuration stays a small value.
- **`FS` is a `vfs.FullFS`**, so a caller that already holds one for its data
  directory hands it over, and a test hands `vfs.NewMem()`.
- **The codes are exported beside the sentinels** because a caller maps them —
  a framework turning `DocumentNotFound` into a 404 reads `CodeDocumentNotFound`.
  Both engines answer the same codes, so one mapping serves both.
- **The SQL engine is `OpenSQL` in this package, not a package of its own**,
  because its refusals ARE this package's: a second package would be a second
  code range for the same meanings.

## README is generated

`README.md` is produced by `gomarkdoc` from the package doc comment in
`docstore.go` (ADR 0008). Regenerate with `make docs-readme`; do not hand-edit it.

## Verification

```sh
bazel test --config=race //pkg/v1/docstore:docstore_test
cd pkg && GOWORK=off go test -race ./v1/docstore/
```
