<!-- updated: 2026-05-18T14:30:00Z -->
# internal/core/

## Purpose

Domain **interfaces** and immutable domain **value types** for the SDK's domains — **codecs, the logger, log-transport writers, cryptographic schemes, byte transforms, OS process supervision, and identifier generation** (ADR 0012, ADR 0013, ADR 0014, ADR 0016, ADR 0024). Core describes "what the SDK's domains are" without prescribing how they are realised — every method body belongs in `internal/service/*`, every public alias belongs in `pkg/v1/*`. Plug-in registries (codec, writer, crypto, transform, id) are the deliberate exception: they carry routing state, no domain logic. `proc` is the deliberate counter-example — no registry: each primitive has a single canonical OS implementation chosen at build time by platform tag. `token` is the second counter-example, and its reason is stronger: a token registry's key would be the `alg` header, which the attacker writes (ADR 0042). ADR 0024 opens the **Phase-B new-domain wave** (identity now; observability/reliability/configuration to follow in ADR 0025–0028). Security tokens land later, as the 13th sibling (ADR 0042).

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
| `id/` | `Generator` port + `Scheme` registry (UUIDv4/v7, ULID, snowflake, NanoID, KSUID; TypeID is constructor-only — it needs a prefix, so nothing is registered under it); canonical-string output (ADR 0024) | `0.2.7.*` |
| `resilience/` | `Runner` port + 7 concrete policies (retry/circuit-breaker/rate-limit/bulkhead/timeout/fallback/hedging); **no registry** (ADR 0026) | `0.2.8.*` |
| `metrics/` | the OpenTelemetry metrics DATA MODEL, implemented from the spec and importing none of its code: instrument interfaces (Counter/UpDownCounter/Gauge/Histogram + the three observable families, variadic in the typed `AttrValue`), the frozen `Meter` + the `UpDownMeter`/`AsyncMeter` siblings (ADR 0039), aggregation `Temporality`, `ResourceValue`/`ScopeValue`, `Exporter` registry, and a `SnapshotValue` keyed name → metric → series; one name + one attribute set = one series, bounded per name; in-mem meter in service (ADR 0027, ADR 0044) | `0.2.9.*` |
| `config/` | `Source` / `Validator` / `Watcher` ports; env+file loader + cross-OS poll watcher in service (ADR 0028) | `0.2.10.*` |
| `net/` | network domain contract: TLS identity (opaque, redacting), listener/handler ports, outbound `Policy`, the Server-Sent Events frame (`SSEEventValue`), the WebSocket wire format (`WSFrameHeaderValue` / `WSCloseCode` / `WSMessageValue` — RFC 6455, ADR 0047) and the drain signal a long-lived handler observes; **no registry** (ADR 0029) | `0.2.11.*` |
| `scheduler/` | time-driven execution contract: `Job` + `Schedule` (both FUNC ports) + `Scheduler`, `EntryValue` / `ResultValue`; **no registry** (ADR 0041) | `0.2.12.*` |
| `lifecycle/` | ordered start/stop contract: `Start` + `Stop` (both FUNC ports) + the three-method `Lifecycle`, `ComponentValue` / `TransitionValue` / the two-valued `Phase`; the Add order IS the dependency order and shutdown is its exact reverse, with **no graph** — a linear sequence already is a topological order (ADR 0050 §D1); a `Stop` is called only for a component whose `Start` returned nil, which is what lets a teardown assume its own construction succeeded; **no registry** (ADR 0050) | `0.2.19.*` |
| `net/` | network domain contract: TLS identity (opaque, redacting), listener/handler ports, outbound `Policy`; **no registry** (ADR 0029) | `0.2.11.*` |
| `token/` | security-token contract: one-method `Issuer` / `Verifier` ports, the redacting immutable `ClaimsValue`, and a closed `Algorithm` enum in which `none` has no representation; **no registry**, because its key would be the attacker-written `alg` header (ADR 0042) | `0.2.13.*` |
| `validation/` | value-checking contract: the `Constraint[T]` FUNC port, the located `ViolationValue`, the `ReportValue` that collects them (its zero value passes, and it is deliberately NOT an `error`), and the path grammar `RootPath`/`JoinField`/`JoinIndex`; **no registry** (ADR 0046) | `0.2.15.*` |
| `cache/` | cache contract: the `Store[V]` port FROZEN at three methods (`Fetch`/`Set`/`Delete`) with `EntryFetcher[V]` / `Tagger` / `Loader[V]` as type-asserted siblings, the `EntryValue[V]` a caller stores, the `Fill[V]` FUNC port, and `NoExpiry` as a third TTL meaning distinct from the zero "use the store's default"; **no registry** — and here the mechanics decide it, since `Store` is generic in `V` and Go has no `map[Name]Store[V]` for an open `V`. `Fetch` keeps the primitive's honest verb: a read mutates (ADR 0049, amending ADR 0025) | `0.2.18.*` |
| `lock/` | mutual-exclusion contract: the `Locker` port FROZEN at two methods (`Acquire`/`TryAcquire`, NEITHER taking a TTL), the `Lease` FROZEN at three (`Fence`/`Extend`/`Release`), and `Deadliner` as the type-asserted sibling a lease implements only when it CAN expire; **no registry** — the two backends differ in exactly that property, so resolving one from a config string would let a typo swap them with every call still succeeding. Ownership is token-checked (a lapsed holder's `Release` releases NOTHING), renewal is refused on a lapsed lease even when nobody has taken the lock, and a fencing token is ISSUED but can only be ENFORCED by the protected resource — stated as a limit, not a footnote (ADR 0052) | `0.2.21.*` |
| `session/` | server-side session contract: a `Store` port FROZEN at five methods with `Sweeper` as its first type-asserted sibling, the `Sealer` that renders an identifier as a cookie value, the opaque redacting `ID`, and the immutable `SessionValue`; **no registry**. `Regenerate` is the only call that binds a subject and always rotates the identifier, so session fixation is prevented by absence rather than by a step (ADR 0045) | `0.2.14.*` |
| `trace/` | distributed-tracing contract: the `Tracer` (1 method) / `Span` (5 methods) ports, both FROZEN, the immutable `SpanContextValue` that travels between processes, the W3C **Trace Context** `traceparent`/`tracestate` format implemented from the ABNF, the OTel span model (`SpanKind` / `StatusValue` / `EventValue` / `LinkValue` / `SpanValue` / `SpansValue`), the `Sampler` + `SpanSink` FUNC ports, the two-method `Carrier` that `http.Header` satisfies with no adapter, and the `SpanExporter` registry. Attributes, `ResourceValue` and `ScopeValue` are ALIASES of `core/metrics`' — they are `common.proto`/`resource.proto`, shared by every signal — while `DefaultScopeName` deliberately is not. The sampling decision is taken ONCE, at the root, and travels in the `sampled` bit (ADR 0051) | `0.2.20.*` |

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
- Grow a **new** sibling without first widening the layer's purpose statement (the `writer` sibling was admitted by ADR 0012; `crypto` by ADR 0013; `transform` by ADR 0014; `proc` by ADR 0016; `id` by ADR 0024, which opens the Phase-B new-domain wave — observability/reliability/configuration land in ADR 0025–0028; `net` by ADR 0029; `scheduler` by ADR 0041; `validation` by ADR 0046, which also records how it FEEDS `config.Validator` instead of replacing it).
- Grow a **new** sibling without first widening the layer's purpose statement (the `writer` sibling was admitted by ADR 0012; `crypto` by ADR 0013; `transform` by ADR 0014; `proc` by ADR 0016; `id` by ADR 0024, which opens the Phase-B new-domain wave — observability/reliability/configuration land in ADR 0025–0028; `net` by ADR 0029; `scheduler` by ADR 0041; `token` by ADR 0042; `session` by ADR 0045; `cache` by ADR 0049, which adds a domain ABOVE the ADR 0025 kernel primitive rather than moving it; `lifecycle` by ADR 0050, which assembles the pieces the other domains already ship rather than adding a new capability).
- Grow a **new** sibling without first widening the layer's purpose statement (the `writer` sibling was admitted by ADR 0012; `crypto` by ADR 0013; `transform` by ADR 0014; `proc` by ADR 0016; `id` by ADR 0024, which opens the Phase-B new-domain wave — observability/reliability/configuration land in ADR 0025–0028; `net` by ADR 0029; `scheduler` by ADR 0041; `token` by ADR 0042; `session` by ADR 0045; `cache` by ADR 0049, which adds a domain ABOVE the ADR 0025 kernel primitive rather than moving it; `lock` by ADR 0052).
- Grow a **new** sibling without first widening the layer's purpose statement (the `writer` sibling was admitted by ADR 0012; `crypto` by ADR 0013; `transform` by ADR 0014; `proc` by ADR 0016; `id` by ADR 0024, which opens the Phase-B new-domain wave — observability/reliability/configuration land in ADR 0025–0028; `net` by ADR 0029; `scheduler` by ADR 0041; `token` by ADR 0042; `session` by ADR 0045; `cache` by ADR 0049, which adds a domain ABOVE the ADR 0025 kernel primitive rather than moving it; `trace` by ADR 0051, which completes observability's third pillar and REUSES this layer's attribute model rather than twinning it).

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
- `metrics/` — see `internal/core/metrics/CLAUDE.md` (observability, ADR 0027 / ADR 0044)
- `config/` — see `internal/core/config/CLAUDE.md` (configuration, ADR 0028)
- `net/` — see `internal/core/net/CLAUDE.md` (network domain, ADR 0029)
- `scheduler/` — see `internal/core/scheduler/CLAUDE.md` (time-driven execution, ADR 0041)
- `lifecycle/` — see `internal/core/lifecycle/CLAUDE.md` (ordered start/stop, ADR 0050 — and why there is deliberately no dependency graph and no autowiring)
- `token/` — see `internal/core/token/CLAUDE.md` (security tokens, ADR 0042)
- `validation/` — see `internal/core/validation/CLAUDE.md` (value validation, ADR 0046)
- `lock/` — see `internal/core/lock/CLAUDE.md` (mutual exclusion, ADR 0052 — ownership, renewal and fencing decided out loud, and the guarantee the domain does NOT make)
- `cache/` — see `internal/core/cache/CLAUDE.md` (the cache domain, ADR 0049 — and why it is NOT `internal/kernel/cache`, which stays exactly what ADR 0025 made it)
- `trace/` — see `internal/core/trace/CLAUDE.md` (distributed tracing, ADR 0051 — the W3C refusals, and why the attribute model is `core/metrics`')
- `session/` — see `internal/core/session/CLAUDE.md` (server-side sessions, ADR 0045 — and the frontier table that keeps this domain from becoming an authentication framework)
- `logger/` — see `internal/core/logger/CLAUDE.md` (README is the human-readable surface doc)
- `logger/level/` — see `internal/core/logger/level/CLAUDE.md`
