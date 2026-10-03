<!-- updated: 2026-09-28T23:30:00Z -->
# pkg/v1/data/docstore/

## Purpose

Public facade over `internal/service/data/docstore` (ADR 0110): typed, keyed JSON
documents with unique and multi-valued secondary indexes, in memory or
persisted through a `vfs.FullFS` — every write durable before it returns, and
a write's cost independent of how many documents the store holds — and the same
contract over SQL, in two tables of a PostgreSQL, MySQL or SQLite database,
every call on the transaction its context carries (ADR 0139). Either engine
keeps, when `Versions` says so, the last versions of each document in the same
durable write as the document (ADR 0143).

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Open[T](cfg, indexes...)` | func | a `*Store[T]`, loaded from `cfg.FS` when set |
| `Unique[T](name, key)` / `Index[T](name, keys)` | func | index declarations |
| `Store[T]` | type alias | `Get`, `List`, `Filter`, `Entries`, `Lookup`, `Find`, `Stats`; `Put`, `Insert`, `Replace`, `Update`, `Delete`; `PutStamped`, `InsertStamped`, `ReplaceStamped`, `UpdateStamped`; `Versions`, `Version`, `RewriteVersions`; `OnWrite`, `OnDelete`; `Fold`, `Close` |
| `Config[T]` | type alias | `Key` (required), `FS` + `Path` (both or neither), `FoldAt`, `Versions` + `Clock` + `Held` |
| `Version` | type alias | `= svcdocstore.VersionValue` — `Number` (from 1), `At`, `Meta`, `JSON` (compact, the caller's copy) |
| `Stamp` | type alias | `= svcdocstore.StampValue` — `Meta` (who, which command), `InPlace` (no version) |
| `IndexSpec[T]` | type alias | `Keys`, `Name`, `Unique` |
| `Entry` | type alias | `= svcdocstore.EntryValue` — `Key`, `JSON` (the caller's copy) |
| `Stats` | type alias | `= svcdocstore.StatsValue` — `Documents`, `Pending`, `Folds`, `FoldError` |
| `DefaultFoldAt` | const | 1024 |
| `OpenSQL[T](cfg, indexes...)` | func | a `*SQLStore[T]` over `cfg.Transactor`; sends no statement |
| `SQLStore[T]` | type alias | `Get`, `List`, `Filter`, `Entries`, `Count`, `Lookup`, `Find`; `Put`, `Insert`, `Replace`, `Update`, `Delete` and their Stamped forms; `Versions`, `Version`, `RewriteVersions`; `OnWrite`, `OnDelete`; `Reindex` — every read and write takes a context (the two hook registrations do not) |
| `SQLConfig[T]` | type alias | `Key`, `Transactor`, `Dialect`, `Table` (required), `IndexKey`, `Versions` + `Clock` + `Held` |
| `SQLMigration(dialect, table, version)` | func | the two tables as one idempotent `sql.Migration` |
| `SQLVersionsMigration(dialect, table, version)` | func | the versions table `<table>___vs`, which a store opened with `Versions` needs; its Down drops it |
| `MaxSQLTableLen` / `MaxSQLKeyLen` / `MaxSQLIndexNameLen` | const | 58 / 1024 / 64 bytes |
| `Code*` | const | `0.3.80.1`–`0.3.80.20` |
| `DocumentNotFound` … `WriteUnconfirmed`, `StatementFailed`, `KeyTooLong`, `VersionsNotKept`, `VersionNotFound`, `VersionsRewriteRefused` | var | the twenty sentinels |

All types are aliases onto the service: there is no port, and the values are
the engines' (ADR 0074, ADR 0139 §D2). The SQL signatures name `pkg/v1/data/sql`'s
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
- **Versions are a store option and four Stamped writes, not a second
  store.** A framework's revisions must be kept in the document's own write —
  a sibling store would be two durable writes, and an erasure two stores to
  rewrite in step (ADR 0143, kit's ADR 0007). The plain writes make versions
  stamped with nothing; the Stamped forms carry who and which command, or
  `InPlace` for a write that must not make one.
- **`Version.JSON` is JSON, not a `T`**: a version made before the type
  changed may no longer decode into it, and a diff reads JSON anyway.

## README is generated

`README.md` is produced by `gomarkdoc` from the package doc comment in
`docstore.go` (ADR 0008). Regenerate with `make docs-readme`; do not hand-edit it.

## Verification

```sh
bazel test --config=race //pkg/v1/data/docstore:docstore_test
cd pkg && GOWORK=off go test -race ./v1/data/docstore/
```
