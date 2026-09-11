# internal/core/id/

## Purpose

Declares the **identifier-generation port** of the SDK: the `Generator`
interface and the process-wide registry mapping a `Scheme` to a registered
`Generator`. A core sibling beside codec / writer / crypto / logger / transform
/ proc, admitted by **ADR 0024**. Mirrors `transform` exactly — the registry
resolves a `Scheme` to a `Generator` the way transform resolves an `Algorithm`
to a `Compressor`.

No generation bodies live here; concrete schemes (UUIDv4/v7, ULID, snowflake,
NanoID, KSUID, TypeID) live under `internal/service/id/`, and all of them but
TypeID self-register via a package-level `var` at import — no `init()`. The canonical external form of
every id is its string rendering, so `Generator.New` returns a `string`.

**TypeID is the one scheme that does NOT self-register**, and the registry is
what makes that legible: a TypeID carries a caller-chosen type prefix, so there
is no generator the SDK could publish under `"typeid"` without inventing that
prefix. `Lookup("typeid")` misses and `New("typeid")` returns `UnknownScheme` —
the correct answer, not a gap (ADR 0038, applying ADR 0031; see
`internal/service/id/CLAUDE.md`).

Code range: `0.2.7.*` (ADR 0024).

## Contents

| File | Surface |
|---|---|
| `id.go` | `Scheme` typed string (`String`/`Known`) + `Generator` interface + `New(scheme)` dispatch |
| `registry.go` | `snapshot.Value`-backed registry: `Register` / `Lookup` / `Available` |
| `codes.go` | `Code*` constants — range 0.2.7.* |
| `errors.go` | `UnknownScheme`, `DuplicateRegistration` sentinels (`errs.Define`) |

## Conventions

- **`snapshot.Value`, not `sync.Map`** — register once at import, read-many (ADR 0011).
- **Registration via package-level `var`, never `init()`**.
- **`Scheme("")` is the reserved invalid zero value** (`Known()` is false).
- Idempotent re-registration of the same generator is a no-op; a distinct
  generator on a taken scheme **panics at boot**.

## Do NOT

- Add a generation body or vendor import here — those live in `service/id/`.
- Add a `MustRegister`/deregistration API.

## Verification

```
bazel test --config=race //internal/core/id:id_test
```
