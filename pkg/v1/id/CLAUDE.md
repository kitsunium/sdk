# pkg/v1/id/

## Purpose

Public facade for SDK identifier generation (ADR 0024). Aliases `core/id`'s
`Scheme`/`Generator`, blank-activates the six registered `service/id` schemes on
import, and exposes ergonomic helpers. Stdlib-only → **dep-light** (no new
vendor entries in `pkg/go.sum`), cross-OS portable.

## Surface

| Symbol | Notes |
|---|---|
| `New(scheme)` | dispatch by `Scheme`; `UnknownScheme` on a missing scheme |
| `UUIDv4` / `UUIDv7` / `ULID` / `Snowflake` / `NanoID` / `KSUID` | named helpers (registered singletons) |
| `NewSnowflake(node)` | explicit-node stateful generator (not global) |
| `NewNanoID(size)` | explicit-length generator; **refuses** `size <= 0` (ADR 0031) |
| `NewTypeID(prefix)` | prefixed generator; **refuses** an invalid prefix (ADR 0031) |
| `ParseKSUID(s)` | → issue time (UTC, second resolution) + 16-byte payload |
| `ParseTypeID(s)` / `FormatTypeID(prefix, uuid)` | exact inverses; `FormatTypeID` relabels an existing UUID |
| `Available()` | sorted registered schemes |
| `UUIDv4Scheme` / `UUIDv7Scheme` / `ULIDScheme` / `SnowflakeScheme` / `NanoIDScheme` / `KSUIDScheme` | frozen scheme keys |
| `TypeIDScheme` | frozen key, deliberately **not** registered — `New` returns `UnknownScheme` |
| `UnknownScheme` / `EntropyFailed` / `ClockBackwards` / `InvalidSize` / `InvalidPrefix` / `Malformed` / `TimestampRange` | sentinels for `errs.HasReason` |

## Conventions

- **Type aliases, not new types** (`Scheme = core/id.Scheme`).
- **README generated** by gomarkdoc from the package doc comment (edit the doc
  comment in `id.go`, run `make docs-readme`); the drift gate enforces it.
- Frozen post-v1.0.0 (signatures + scheme string values).

## Why TypeID takes a constructor, not a scheme key

`New(TypeIDScheme)` returns `UnknownScheme` on purpose. A TypeID's prefix names
the caller's entity, and no prefix the SDK invented would be theirs — a
registered singleton would have to mint `_01h2…` or guess, both of which ADR
0031 forbids. `TypeIDScheme` stays exported because `Generator.Scheme()` reports
it; it is the one key `New` cannot resolve.

`NewNanoID` and `NewTypeID` are the only constructors here returning
`(Generator, error)`. They refuse rather than clamp because the knob they take
*is* the caller's intent — unlike `NewSnowflake`, whose node id has a defensible
derived default.

## Do NOT

- Surface raw entropy/clock errors' `PrivateOf` to end users.
- Rename a `Scheme` string value — part of the frozen contract.
- Register a `typeid` generator to make `New(TypeIDScheme)` "work".

## Verification

```
bazel test --config=race //pkg/v1/id:id_test
```
