<!-- updated: 2026-05-18T14:30:00Z -->
# internal/core/

## Purpose

Domain **interfaces** and immutable domain **value types** for the SDK's domains — **codecs, the logger, log-transport writers, cryptographic schemes, byte transforms, OS process supervision, and identifier generation** (ADR 0012, ADR 0013, ADR 0014, ADR 0016, ADR 0024). Core describes "what the SDK's domains are" without prescribing how they are realised — every method body belongs in `internal/service/*`, every public alias belongs in `pkg/v1/*`. Plug-in registries (codec, writer, crypto, transform, id) are the deliberate exception: they carry routing state, no domain logic. `proc` is the deliberate counter-example — no registry: each primitive has a single canonical OS implementation chosen at build time by platform tag. ADR 0024 opens the **Phase-B new-domain wave** (identity now; observability/reliability/configuration to follow in ADR 0025–0028).

## Contents

| Package | Purpose | Code range (ADR 0005/0006/0012/0013) |
|---|---|---|
| `codec/` | `Codec` / `StreamingCodec` / `Encoder` / `Decoder` / `Appender` + process-wide registry, `Format` value type | `0.2.2.*` |
| `writer/` | `Factory` / `Name` / `Config` + process-wide registry mapping a writer name to a `Sink`-producing factory (ADR 0012) | `0.2.3.*` |
| `crypto/` | eight registries on one `Algorithm` keyspace: `AEAD` + redacting `Key` (`Seal`/`Open`), the non-authenticated `Hasher` (`Sum`/`SumHex`/`NewHash`), the `Signer` (`Sign`/`Verify`/`GenerateKey`), the key-separation `Deriver` (`Subkey`), the password-storage `PasswordHasher` (`HashPassword`/`VerifyPassword`/`NeedsRehash`), the detached `MAC` (`MACTag`/`MACVerify`), the `Agreement` DH port, and the chunked `StreamSealer` (ADR 0013 + ADR 0014) | `0.2.4.*` |
| `transform/` | `Compressor` port + process-wide registry mapping an `Algorithm` to a `Compressor` (`Compress`/`Decompress`); a parallel registry, never a codec `Format` (ADR 0014) | `0.2.5.*` |
| `logger/` | `Logger` / `Handler` / `Sink` / `Encoder` interfaces, `RecordEvent`, `AttrValue`, `Value`, `Kind` | `0.2.16.*` (reserved) |
| `logger/level/` | `Level int8` + `Debug`/`Info`/`Warn`/`Error` constants + `String()` | `0.2.17.*` (reserved) |
| `proc/` | OS process-supervision foundation: `Process` / `Reaper` / `Group` / `Listener` ports + `Spec` / `ExitValue` / `LimitValue` / `NotificationValue` / `Signal` / `Resource` value types; no registry (build-tag selection) (ADR 0016) | `0.2.6.*` |
| `id/` | `Generator` port + `Scheme` registry (UUIDv4/v7, ULID, snowflake); canonical-string output (ADR 0024) | `0.2.7.*` |
| `resilience/` | `Runner` port + 5 concrete policies (retry/circuit-breaker/rate-limit/bulkhead/timeout); **no registry** (ADR 0026) | `0.2.8.*` |
| `metrics/` | instrument interfaces (Counter/Gauge/Histogram) + `Meter` + `Exporter` registry; in-mem meter in service (ADR 0027) | `0.2.9.*` |
| `config/` | `Source` / `Validator` / `Watcher` ports; env+file loader + cross-OS poll watcher in service (ADR 0028) | `0.2.10.*` |
| `net/` | network domain contract: TLS identity (opaque, redacting), listener/handler ports, outbound `Policy`; **no registry** (ADR 0029) | `0.2.11.*` |

`Major=0` (internal), `Layer=2` (core). The codec registry ships codes today (`CodeDuplicateRegistration` 0.2.2.1, plus 0.2.2.2-4 reserved for future Marshal/Unmarshal sentinels); the writer registry ships `0.2.3.*` (ADR 0012). Logger codes will land alongside service-layer wiring.

## Module

Single Go module `github.com/kitsunium/sdk/internal/core` — one `go.mod`, one `go.sum`. `replace` resolves `internal/kernel` to `../kernel`.

## Conventions

- **Interface-first.** Core packages expose interfaces + immutable value structs. Any method with a non-trivial body belongs in `internal/service/*`.
- **Role-suffix on exported structs** (ktn-linter `KTN-STRUCT-ROLE`): `AttrValue`, `RecordEvent`, `Value`. Short aliases (`Attr = AttrValue`) re-exported at `pkg/v1/logger`.
- **Imports allowed**: stdlib + `internal/kernel/*`. Never `internal/service/*`, never `pkg/*`.
- **Plug-in registries** (codec, writer): constructors carry `// IFACE-PLUGIN:` markers — concrete types stay unexported; the registry hands instances back behind the domain interface (`Codec`, `Factory`).

## Do NOT

- Add concrete runtime types with stateful methods here. The `codec` / `writer` / `crypto` / `transform` / `id` registries' `snapshot.Value`-backed lookups are the deliberate exceptions — they carry no domain logic, only routing.
- Import `context` outside of interface signatures — **except for a single-method function port**, i.e. a named `func(ctx context.Context) …` type that IS the contract (`resilience.Operation`, ADR 0026). Such a type is a declaration, not plumbing: it is the function-shaped equivalent of a one-method interface, and the Go stdlib uses the same shape (`http.HandlerFunc`). Forcing it into an interface would make every call site write an adapter for no gain. This exception does NOT admit `context` in struct fields, value types, or package-level state.
- Reach upward into `internal/service/*` or `pkg/*`.
- Grow a **new** sibling without first widening the layer's purpose statement (the `writer` sibling was admitted by ADR 0012; `crypto` by ADR 0013; `transform` by ADR 0014; `proc` by ADR 0016; `id` by ADR 0024, which opens the Phase-B new-domain wave — observability/reliability/configuration land in ADR 0025–0028).

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
- `writer/` — see `internal/core/writer/CLAUDE.md`
- `crypto/` — see `internal/core/crypto/CLAUDE.md`
- `transform/` — see `internal/core/transform/CLAUDE.md`
- `proc/` — see `internal/core/proc/CLAUDE.md` (OS process-supervision foundation, ADR 0016)
- `id/` — see `internal/core/id/CLAUDE.md` (identifier generation, ADR 0024)
- `resilience/` — see `internal/core/resilience/CLAUDE.md` (reliability policies, ADR 0026)
- `metrics/` — see `internal/core/metrics/CLAUDE.md` (observability, ADR 0027)
- `config/` — see `internal/core/config/CLAUDE.md` (configuration, ADR 0028)
- `net/` — see `internal/core/net/CLAUDE.md` (network domain, ADR 0029)
- `logger/` — see `internal/core/logger/CLAUDE.md` (README is the human-readable surface doc)
- `logger/level/` — see `internal/core/logger/level/CLAUDE.md`
