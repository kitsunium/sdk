# pkg/v1/sql/

## Purpose

The **public facade** for the relational-database domain (ADR 0055): type
aliases onto `internal/core/sql` and `internal/service/sql`, the sentinel
re-exports, thin delegating constructors, and one ergonomic helper.

`README.md` is **generated** from the package doc comment by `gomarkdoc`
(CLAUDE.md rule 10 / ADR 0008). Edit `sql.go`'s package comment, then run
`make docs-readme`. Never hand-edit `README.md`.

## Surface

| Kind | Names |
|---|---|
| Port aliases | `Executor`, `Preparer`, `TxFunc`, `Transactor`, `Checker`, `Migrator`, `Step` |
| Value aliases | `TxOptions` (= `coresql.TxOptionsValue`), `Migration` (= `coresql.MigrationValue`), `Dialect` |
| Config aliases | `Config`, `PoolConfig`, `MigrateConfig` |
| Constants | `DialectPostgres`, `DialectMySQL`, `DialectSQLite` |
| Constructors | `NewTransactor`, `NewChecker`, `NewMigrator` |
| Functions | `ParseDialect`, `Irreversible`, `Statements`, `Transact` |
| Sentinels | the 5 core + 17 service `errs.Define` values, re-exported |

## Conventions

- **Aliases, not wrappers.** `type Executor = coresql.Executor` — a *copy*
  would drift, and a consumer's own implementation would stop satisfying the
  internal port. `TestMigrationValidatesThroughTheAlias` pins that `Migration`
  really is the core value type.
- **The `Value` suffix is dropped at the boundary.** `coresql.TxOptionsValue`
  publishes as `sql.TxOptions`, `coresql.MigrationValue` as `sql.Migration`.
  The internal suffix disambiguates a value from its port inside the layer; a
  consumer has no such collision.
- **`Transact` is the only ergonomic helper**, and it does exactly one thing:
  pass the zero `TxOptions`. `TestTransactPassesTheZeroOptions` is the guard —
  it exists to stop someone "improving" the helper into a read-only default.
  The *port* keeps its options parameter so it never needs a second method
  (ADR 0039).
- **No driver is imported here either**, so a `pkg` consumer stays dep-light.
  The consumer imports the driver it wants and hands over a `*sql.DB`.
- **The doc comment's examples are executable claims.**
  `TestTheDocumentedCodeMatchingActuallyCompiles` runs the two error-matching
  spellings the package doc offers, because a doc naming a symbol that does not
  exist is exactly the rule-11 divergence this repo treats as a defect.
- **`Public` messages are re-checked at this layer.**
  `TestNoPublicMessageReachesTheConsumerWithInfrastructure` asserts every
  re-exported sentinel's `Public` is non-empty, ≤ 120 runes, newline-free and
  free of DSN/SQL vocabulary. It duplicates the core/service tests on purpose:
  this is the layer a consumer actually renders to a user.

## Do NOT

- **Hand-edit `README.md`.** It is generated (rule 10); `make lint` blocks a
  commit where it differs from what `gomarkdoc` would produce now.
- **Add behaviour here.** A constructor in this package delegates and does
  nothing else. Logic belongs in `internal/service/sql`, shapes in
  `internal/core/sql`.
- **Export the `Code*` constants.** The convention across `pkg/v1` is the
  sentinel plus `.Code()`; a second spelling of the same number is a second
  thing to keep in step.
- **Re-declare an alias as a struct or interface of its own.** See ADR 0039 /
  0040 for what a published shape may and may not do.

## Verification

```
cd pkg && GOWORK=off go test -race ./v1/sql
make docs-readme   # regenerates README.md from the package doc comment
```
