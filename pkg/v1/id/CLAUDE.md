# pkg/v1/id/

## Purpose

Public facade for SDK identifier generation (ADR 0024). Aliases `core/id`'s
`Scheme`/`Generator`, blank-activates the four `service/id` schemes on import,
and exposes ergonomic helpers. Stdlib-only → **dep-light** (no new vendor
entries in `pkg/go.sum`), cross-OS portable.

## Surface

| Symbol | Notes |
|---|---|
| `New(scheme)` | dispatch by `Scheme`; `UnknownScheme` on a missing scheme |
| `UUIDv4` / `UUIDv7` / `ULID` / `Snowflake` | named helpers (registered singletons) |
| `NewSnowflake(node)` | explicit-node stateful generator (not global) |
| `Available()` | sorted registered schemes |
| `UUIDv4Scheme` / `UUIDv7Scheme` / `ULIDScheme` / `SnowflakeScheme` | frozen scheme keys |
| `UnknownScheme` / `EntropyFailed` / `ClockBackwards` | sentinels for `errs.HasReason` |

## Conventions

- **Type aliases, not new types** (`Scheme = core/id.Scheme`).
- **README generated** by gomarkdoc from the package doc comment (edit the doc
  comment in `id.go`, run `make docs-readme`); the drift gate enforces it.
- Frozen post-v1.0.0 (signatures + scheme string values).

## Do NOT

- Surface raw entropy/clock errors' `PrivateOf` to end users.
- Rename a `Scheme` string value — part of the frozen contract.

## Verification

```
bazel test --config=race //pkg/v1/id:id_test
```
