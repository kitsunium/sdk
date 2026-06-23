# internal/service/id/

## Purpose

Concrete `core/id.Generator` implementations over stdlib `crypto/rand` + the
kernel `clock`. Blank-importing self-registers every stateless scheme; snowflake
also offers a stateful per-node constructor. **No vendor deps** (stdlib only).
ADR 0024. Code range: `0.3.39.*`.

## Contents

| File | Scheme | Notes |
|---|---|---|
| `common.go` | — | shared consts + `readRandom`/`formatUUID`/`setUUIDBits`/`putUint48BE` |
| `uuidv4.go` | `uuidv4` | RFC 9562 §5.4, fully random |
| `uuidv7.go` | `uuidv7` | RFC 9562 §5.7, 48-bit ms prefix (k-sortable) |
| `ulid.go` | `ulid` | 48-bit ms + 80-bit random, Crockford base32 (26 chars) |
| `snowflake.go` | `snowflake` | stateful: 41-bit ms + 10-bit node + 12-bit seq → decimal; `NewSnowflake(node)` |
| `codes.go` | — | `0.3.39.*` (ID_ENTROPY_FAILED, ID_CLOCK_BACKWARDS) |
| `errors.go` | — | sentinels |

## Conventions

- **Registration via `var X = id.Register(...)`, never `init()`.**
- Stateless singletons (uuidv4/v7/ulid) ; snowflake is per-instance stateful
  (mutex + sequence), with a default-node singleton + `NewSnowflake(node)`.
- Entropy failures wrap `crypto/rand` via `errs.Wrap` (ID_ENTROPY_FAILED).
- Cross-OS: 100 % portable (crypto/rand, time, hashing) — no OS-specific code.

## Do NOT

- Add an `init()`. Use the package-level `var = id.Register(...)`.
- Claim zero-alloc: string rendering allocates by design.

## Verification

```
bazel test --config=race //internal/service/id:id_test
```
