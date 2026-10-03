<!-- updated: 2026-10-03T12:00:00Z -->
# internal/core/app/id/

## Purpose

Declares the **identifier-generation port** of the SDK: the `Generator`
interface and the process-wide registry mapping a `Scheme` to a registered
`Generator`. A core sibling beside codec / writer / crypto / logger / transform
/ proc, admitted by **ADR 0024**. Mirrors `transform` exactly — the registry
resolves a `Scheme` to a `Generator` the way transform resolves an `Algorithm`
to a `Compressor`.

No generation bodies live here; concrete schemes (UUIDv4/v7, ULID, snowflake,
NanoID, KSUID, TypeID) live under `internal/service/app/id/`, and all of them but
TypeID self-register via a package-level `var` at import — no `init()`. The canonical external form of
every id is its string rendering, so `Generator.New` returns a `string`.

**TypeID is the one scheme that does NOT self-register**, and the registry is
what makes that legible: a TypeID carries a caller-chosen type prefix, so there
is no generator the SDK could publish under `"typeid"` without inventing that
prefix. `Lookup("typeid")` misses and `New("typeid")` returns `UnknownScheme` —
the correct answer, not a gap (ADR 0038, applying ADR 0031; see
`internal/service/app/id/CLAUDE.md`).

Code range: `0.2.7.*` (ADR 0024).

## Contents

| File | Surface |
|---|---|
| `id.go` | `Scheme` typed string (`String`/`Known`) + `Generator` interface + `New(scheme)` dispatch |
| `registry.go` | the registry, an instance of `kernel/plugin.Registry` (ADR 0159): `Register` / `Lookup` / `Available`; what stays here is the refusal — the reserved empty `Scheme` and the conflict, both `DUPLICATE_REGISTRATION` with a `scheme` field |
| `codes.go` | `Code*` constants — range 0.2.7.* |
| `errors.go` | `UnknownScheme`, `DuplicateRegistration` sentinels (`errs.Define`) |

## Conventions

- **The table is the kernel's `plugin.Registry`, not a copy of it** — register
  once at import, read-many: the check and the insert are one atomic step and a
  `Lookup` is a lock-free snapshot read (ADR 0011, ADR 0159).
- **Registration via package-level `var`, never `init()`**.
- **`Scheme("")` is the reserved invalid zero value** (`Known()` is false).
- Idempotent re-registration of the same generator is a no-op; a distinct
  generator on a taken scheme **panics at boot**. So do a generator whose
  `Scheme()` is empty and an unusable one — a nil, a typed nil or a
  non-comparable value (`plugin.Unusable`, ADR 0071) — each with the
  `DUPLICATE_REGISTRATION` bracket.

## Do NOT

- Add a generation body or vendor import here — those live in `service/app/id/`.
- Add a `MustRegister`/deregistration API.

## Verification

```
bazel test --config=race //internal/core/app/id:id_test
```
