<!-- updated: 2026-05-18T14:30:00Z -->
# internal/core/

## Purpose

Domain **interfaces** and immutable domain **value types**. Core describes "what the SDK's domains are" without prescribing how they are realised — every method body belongs in `internal/service/*`, every public alias belongs in `pkg/v1/*`.

## Contents

| Package | Purpose | Code range (ADR 0005/0006) |
|---|---|---|
| `codec/` | `Codec` / `StreamingCodec` / `Encoder` / `Decoder` / `Appender` + process-wide registry, `Format` value type | `0.2.2.*` |
| `logger/` | `Logger` / `Handler` / `Sink` / `Encoder` interfaces, `RecordEvent`, `AttrValue`, `Value`, `Kind` | `0.2.16.*` (reserved) |
| `logger/level/` | `Level int8` + `Debug`/`Info`/`Warn`/`Error` constants + `String()` | `0.2.17.*` (reserved) |

`Major=0` (internal), `Layer=2` (core). The codec registry is the only emitter that ships codes today (`CodeDuplicateRegistration` 0.2.2.1, plus 0.2.2.2-4 reserved for future Marshal/Unmarshal sentinels). Logger codes will land alongside service-layer wiring.

## Module

Single Go module `github.com/kitsunium/sdk/internal/core` — one `go.mod`, one `go.sum`. `replace` resolves `internal/kernel` to `../kernel`.

## Conventions

- **Interface-first.** Core packages expose interfaces + immutable value structs. Any method with a non-trivial body belongs in `internal/service/*`.
- **Role-suffix on exported structs** (ktn-linter `KTN-STRUCT-ROLE`): `AttrValue`, `RecordEvent`, `Value`. Short aliases (`Attr = AttrValue`) re-exported at `pkg/v1/logger`.
- **Imports allowed**: stdlib + `internal/kernel/*`. Never `internal/service/*`, never `pkg/*`.
- **Plug-in registries** (codec): constructors carry `// IFACE-PLUGIN:` markers — concrete types stay unexported per format; the registry hands instances back behind the `Codec` interface.

## Do NOT

- Add concrete runtime types with stateful methods here. The `codec` registry's `atomic.Pointer[map]`-backed lookup is the one exception — it carries no domain logic, only routing.
- Import `context` outside of interface signatures.
- Reach upward into `internal/service/*` or `pkg/*`.
- Grow a third sibling beside `codec/` and `logger/` without first widening the layer's purpose statement.

## Verification

```
# Primary (Bazel)
bazel test --config=race //internal/core/...

# Fallback (go test)
cd internal/core && GOWORK=off go test -race -cover ./...
# expected: codec ~100% (registry + format), logger has interface-only files +
# value/kind tests, logger/level 100%.
```

## Subtree

- `codec/` — see `internal/core/codec/CLAUDE.md`
- `logger/` — see `internal/core/logger/CLAUDE.md` (README is the human-readable surface doc)
- `logger/level/` — see `internal/core/logger/level/CLAUDE.md`
