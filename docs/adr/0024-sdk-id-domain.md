# ADR 0024 — Identifier-generation domain (`id`)

- **Status**: Accepted
- **Date**: 2026-06-24
- **Deciders**: SDK maintainers
- **Related**: ADR 0001 (multi-module layout), ADR 0005/0006 (error-code registry), ADR 0011 (snapshot registry), ADR 0014 (transform — the mirrored registry shape)
- **Amends**: the `internal/core` purpose statement (admits the first Phase-B sibling)

## Context

The SDK's six domains (logger, codec, errs, crypto, transform, proc) cover
serialisation, observability-of-errors, and OS supervision, but offer no
**identifier generation** — a need every downstream re-implements (request ids,
correlation ids, sortable keys). UUIDs, ULIDs, and snowflakes are small,
stdlib-feasible, and a natural first "new micro-domain" to validate the mold for
Phase B (the new-domain backlog).

## Decision

1. **Add `internal/core/id`** — a core sibling declaring the `Generator`
   contract and a `snapshot.Value`-backed registry keyed by `Scheme`, mirroring
   `transform`. `Generator.New() (string, error)` returns the **canonical string
   form** (uniform across schemes; the 95% consumer need). `New(scheme)`
   dispatches; unknown schemes return `UnknownScheme`.
2. **`internal/service/id`** ships four stdlib-only generators, self-registered
   via package-level `var`:
   - `uuidv4` (RFC 9562 §5.4, fully random),
   - `uuidv7` (RFC 9562 §5.7, 48-bit ms prefix → k-sortable),
   - `ulid` (48-bit ms + 80-bit random, 26-char Crockford base32),
   - `snowflake` (41-bit ms + 10-bit node + 12-bit sequence → decimal; stateful,
     with a default-node singleton + `NewSnowflake(node)` for explicit nodes).
3. **`pkg/v1/id`** re-exports the port (aliases) + ergonomic helpers
   (`UUIDv4`/`UUIDv7`/`ULID`/`Snowflake`/`New`/`NewSnowflake`/`Available`).
4. **Error block `0.2.7.*`** (core) / `0.3.39.*` (service): `UNKNOWN_SCHEME`,
   `DUPLICATE_REGISTRATION` (core); `ID_ENTROPY_FAILED`, `ID_CLOCK_BACKWARDS`
   (service).

### Core-sibling authorization

This is the first Phase-B sibling beyond the original six. Per
`internal/core/CLAUDE.md` §Do NOT, a new sibling requires widening the layer's
purpose statement; this ADR authorizes admitting the **identity** concern (and,
by the same Phase-B amendment, observability/reliability/configuration in
ADR 0025–0028). The `internal/core/CLAUDE.md` and root `CLAUDE.md` purpose
statements are updated in the same commit.

### Cross-platform (ADR 0018)

100% portable Go (`crypto/rand`, `time`, hashing). No OS-specific code; the
build bar is trivially met on all 8 GOOS. Snowflake's default node id derives
from hostname+pid via an inline FNV-1a — no syscall, no MAC address.

## Consequences

- Registry now has a 7th core sibling; `pkg/v1` gains a dep-light `id` facade
  (stdlib only — no new go.sum vendor entries). Docs (counts, tables, ADR
  registry, `docs/error-codes.yaml`) updated per rule 11.
- `Generator.New` returning `string` (not a typed `ID`) is a deliberate
  simplification: raw-byte accessors are deferred (see below).

## Alternatives considered

- **`string`-returning vs a typed `ID` value** — a typed `ID` (16-byte array +
  accessors) was rejected for v1: schemes render differently (UUID dashed-hex,
  ULID base32, snowflake decimal), so a uniform `string` is the honest common
  contract. Raw bytes are a deferred accessor.
- **No registry (plain functions)** — rejected: the registry validates the
  Phase-B mold and lets downstreams register custom schemes (NanoID, KSUID)
  without a core change.
- **kernel placement** — rejected: UUID/ULID/snowflake carry domain vocabulary,
  failing the kernel "generic, no domain vocab" gate (unlike `cache`, ADR 0027).

## Deferred

- Raw-byte accessor / `Parse(scheme, s)` round-trip.
- Monotonic-random ULID mode (strict intra-ms ordering).
- Additional schemes (NanoID, KSUID) — additive via the registry.

## References

- Impl: `internal/core/id/`, `internal/service/id/`, `pkg/v1/id/`.
- ADR 0014 (transform registry shape), ADR 0011 (snapshot), ADR 0018 (portability).
