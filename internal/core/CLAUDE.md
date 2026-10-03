<!-- updated: 2026-10-03T06:30:00Z -->
# internal/core/

## Purpose

Domain **interfaces** and immutable domain **value types** for the SDK's domains — one row each in the table below, from **codecs, the logger, log-transport writers, cryptographic schemes, byte transforms, OS process supervision, and identifier generation** (ADR 0012, ADR 0013, ADR 0014, ADR 0016, ADR 0024) to every domain a later ADR admitted. Core describes "what the SDK's domains are" without prescribing how they are realised — every method body belongs in `internal/service/*`, every public alias belongs in `pkg/v1/*`. Plug-in registries (codec, writer, crypto, transform, id, and the metrics exporter, trace exporter and view engine registries) are the deliberate exception: they carry routing state, no domain logic. `proc` is the deliberate counter-example — no registry: each primitive has a single canonical OS implementation chosen at build time by platform tag. `token` is the second counter-example, and its reason is stronger: a token registry's key would be the `alg` header, which the attacker writes (ADR 0042). `statemachine` (ADR 0120) is a sibling whose two ports the CALLER implements — the store of the entities a machine drives and the journal of its records — so its contract is ports and the values they speak, and the engine is `internal/service/app/statemachine`.

## Contents

Packages are grouped by FAMILY, the same families in every layer (ADR 0155
§2): a domain lives at `<family>/<domain>`. Where a family holds several
domains, its directory holds no Go code — it is a prefix, not a package — and
carries a `CLAUDE.md` naming its members and the rule that put them together:
`security/` (`authz`, `secret`, `session`, `token`), `observe/` (`logger`
with `logger/level` and `logger/writer` beneath it, `metrics`, `otel`,
`trace`), `data/` (`cache`, `codec` with `codec/scratch` beneath it,
`queue`, `sql`, `transform`, `vfs`) and `app/` (`cli`, `config`, `events`,
`health`, `i18n`, `id`, `lifecycle`, `lock`, `mail`, `resilience`,
`scheduler`, `statemachine`, `validation`, `view`). Where the family's core is
one package, that package sits at the family's path (`crypto/`, `net/`,
`proc/`), and a service package beneath it with codes, values or ports of its
own is mirrored beneath it at the same path (`crypto/key/jwk`, `proc/ipc` —
ADR 0160). No domain sits at the root of this layer any more, and the root
`CLAUDE.md` architecture tree names every package by its path
(`check-domain-docs.sh`).

| Package | Purpose | Code range (ADR 0005/0006/0012/0013) |
|---|---|---|
| `data/codec/` | `Codec` / `StreamingCodec` / `Encoder` / `Decoder` / `Appender` + process-wide registry, `Format` value type | `0.2.2.*` |
| `observe/logger/writer/` | `Factory` / `Name` / `Config` + process-wide registry mapping a writer name to a `Sink`-producing factory (ADR 0012); beneath `observe/logger/` because what it produces is the logger's `Sink` | `0.2.3.*` |
| `crypto/` | eight registries on one `Algorithm` keyspace: `AEAD` + redacting `Key` (`Seal`/`Open`), the non-authenticated `Hasher` (`Sum`/`SumHex`/`NewHash`), the `Signer` (`Sign`/`Verify`/`GenerateKey`), the key-separation `Deriver` (`Subkey`), the password-storage `PasswordHasher` (`HashPassword`/`VerifyPassword`/`NeedsRehash`), the detached `MAC` (`MACTag`/`MACVerify`), the `Agreement` DH port, and the chunked `StreamSealer` (ADR 0013 + ADR 0014) | `0.2.4.*` |
| `crypto/key/jwk/` | the JSON Web Key format's codes and sentinels and nothing else — the format itself is `internal/service/crypto/key/jwk`, at the mirrored path (ADR 0160); `crypto/key/` holds no Go code | `0.3.42.*` (allocated to the service, value unchanged) |
| `data/transform/` | `Compressor` port + process-wide registry mapping an `Algorithm` to a `Compressor` (`Compress`/`Decompress`); a parallel registry, never a codec `Format` (ADR 0014) | `0.2.5.*` |
| `observe/logger/` | `Logger` / `Handler` / `Sink` / `Encoder` interfaces, `RecordEvent` (incl. `TraceContext`), `AttrValue`, `Value`, `Kind`, `TraceContextValue` + the `TraceContextSource` FUNC port (ADR 0062 — stdlib-only: the trace domain is NOT imported here) | `0.2.1.*` (reserved) |
| `app/mail/` | electronic-mail port: `Transport` frozen at one method, `BatchSender`/`Outbox` siblings, the message as a value (`MessageValue`/`AddressValue`/`AttachmentValue`/`HeaderFieldValue`), `EnvelopeValue` where Bcc becomes RCPT TO and no header, and the injection gate every writer runs — CR/LF/NUL in a header is a typed refusal, never a repair; **no registry** (ADR 0064) | `0.2.31.*` |
| `observe/logger/level/` | `Level int8` + `Debug`/`Info`/`Warn`/`Error` constants + `String()` | `0.2.17.*` |
| `proc/` | OS process-supervision foundation: `Process` / `Reaper` / `Group` / `Listener` ports + `Spec` / `ExitValue` / `LimitValue` / `NotificationValue` / `Signal` / `Resource` value types; no registry (build-tag selection) (ADR 0016) | `0.2.6.*` |
| `proc/ipc/` | the private socket's contract: the `Listener` port (frozen at five — `Accept`/`Addr`/`Close`/`Path`/`Refused`) and the `Dialer` port (frozen at one), `PeerValue` (the kernel's word on the peer, `Verified` false where only the directory admitted it) and `Conn`; the engine is `internal/service/proc/ipc`, and its `Config` stays there (ADR 0074, ADR 0148, ADR 0160) | `0.3.91.*` (allocated to the service, value unchanged) |
| `data/queue/` | asynchronous DURABLE message-queue contract: the `Broker` port FROZEN at four methods (`Publish`/`Receive`/`Ack`/`Nack`), the `Handler` FUNC port, `MessageValue`/`DeliveryValue`/`LeaseValue`/`ReceiptValue`/`NackValue`/`DeadLetterValue`, `PolicyValue` + its shared guard (with `MaxRetryDelay`, a retry delay that grows — ADR 0151), and `DeadLetterReader`/`LeaseExtender`/`Waker` (+ `WakeValue`)/`Rejecter`/`DeadLetterManager` as the five ADR 0039 siblings — the third lets an idle consumer sleep until a publication or a due instant instead of polling (ADR 0104), the fourth dead-letters a leased message at once and the fifth replays or deletes a dead letter (ADR 0151); `DoNotRetry` marks a failure no retry can fix on the wrap trail, so the cause stays the origin a dead letter records; **no registry**. **`queue` is not `events`** — many processes, asynchronous, a consumer's goroutine, durable, retried, dead-lettered; it is the right-hand column of ADR 0053's frontier table, and the routing key is a NAME rather than a Go type precisely because a type has no identity in another process. Delivery is AT-LEAST-ONCE and the type says so: `Deliveries` counts from 1 on every delivery. Both ADR 0031 branches live in `PolicyValue` (ADR 0054) | `0.2.23.*` |
| `app/id/` | `Generator` port + `Scheme` registry (UUIDv4/v7, ULID, snowflake, NanoID, KSUID; TypeID is constructor-only — it needs a prefix, so nothing is registered under it); canonical-string output (ADR 0024) | `0.2.7.*` |
| `app/i18n/` | message-translation port: `Catalog` frozen at two methods with `KeyLister`/`Fallbacker` as ADR 0039 siblings, `TagValue` (BCP 47 subset), `MessageValue` (compiled at load), CLDR `Form` (`FormOther` is the zero), `CountValue` (the CLDR operands, carrying the DISPLAY precision) and `Args map[string]string`; **no registry** (ADR 0063) | `0.2.30.*` |
| `app/resilience/` | `Runner` port + 7 concrete policies (retry/circuit-breaker/rate-limit/bulkhead/timeout/fallback/hedging); **no registry** (ADR 0026) | `0.2.8.*` |
| `observe/metrics/` | the OpenTelemetry metrics DATA MODEL, implemented from the spec and importing none of its code: instrument interfaces (Counter/UpDownCounter/Gauge/Histogram + the three observable families, variadic in `observe/otel/`'s typed `AttrValue`), the frozen `Meter` + the `UpDownMeter`/`AsyncMeter`/`Describer` siblings (ADR 0039 — `Describer` is type-asserted and deliberately outside `FullMeter`), aggregation `Temporality`, `Exporter` registry, and a `SnapshotValue` keyed name → metric → series carrying `observe/otel/`'s Resource and Scope; this signal keeps its own attribute refusal (`InvalidAttribute`), `DefaultScopeName` and `OverflowAttrKey`; one name + one attribute set = one series, bounded per name; in-mem meter in service (ADR 0027, ADR 0044) | `0.2.9.*` |
| `observe/otel/` | the OpenTelemetry types every signal shares — the typed `AttrValue` (`common.proto` `KeyValue`/`AnyValue`), `ResourceValue` (+ `ServiceNameKey`/`UnknownService`) and `ScopeValue` — with `observe/metrics/` and `observe/trace/` both above it, so neither signal borrows the other's model (the extraction ADR 0051 §Decision 2 named). It owns NO code: `ValidateAttrs` / `SortAttrs` / `NormalizeResource` panic with the sentinel their CALLER passes, and `NormalizeScope` takes the signal's own default name | none |
| `app/config/` | `Source` / `Validator` / `Watcher` ports; env+file loader + cross-OS poll watcher in service (ADR 0028 + ADR 0061) | `0.2.10.*` |
| `net/` | network domain contract: TLS identity (opaque, redacting), listener/handler ports, outbound `Policy`, the Server-Sent Events frame as a value (`SSEEventValue` + `Validate`), the WebSocket protocol's vocabulary (`WSOpCode` / `WSCloseCode` with `Sendable`/`Echoable` / `WSMessageValue` and the handshake constants — RFC 6455, ADR 0047) and the drain signal a long-lived handler observes; the wire formats themselves are `service/net/{websocket,sse}`'s (ADR 0160 §4); **no registry** (ADR 0029) | `0.2.11.*` |
| `app/scheduler/` | time-driven execution contract: `Job` + `Schedule` (both FUNC ports) + `Scheduler`, `EntryValue` / `ResultValue`; **no registry** (ADR 0041) | `0.2.12.*` |
| `app/lifecycle/` | ordered start/stop contract: `Start` + `Stop` (both FUNC ports) + the three-method `Lifecycle`, `ComponentValue` / `TransitionValue` / the two-valued `Phase`; the Add order IS the dependency order and shutdown is its exact reverse, with **no graph** — a linear sequence already is a topological order (ADR 0050 §D1); a `Stop` is called only for a component whose `Start` returned nil, which is what lets a teardown assume its own construction succeeded; **no registry** (ADR 0050) | `0.2.19.*` |
| `security/token/` | security-token contract: one-method `Issuer` / `Verifier` ports, the redacting immutable `ClaimsValue`, and a closed `Algorithm` enum in which `none` has no representation; **no registry**, because its key would be the attacker-written `alg` header (ADR 0042) | `0.2.13.*` |
| `app/validation/` | value-checking contract: the `Constraint[T]` FUNC port, the located `ViolationValue`, the `ReportValue` that collects them (its zero value passes, and it is deliberately NOT an `error`), and the path grammar `RootPath`/`JoinField`/`JoinIndex`; **no registry** (ADR 0046) | `0.2.15.*` |
| `data/vfs/` | filesystem contract: `FS` (a type ALIAS of `io/fs.FS` — reading is the stdlib's, and there is deliberately no `vfs.Walk`/`vfs.Glob`), `WritableFS` FROZEN at four write verbs, `AtomicWriter` as its ADR 0039 capability sibling, `FullFS` the union, plus the two shared guards `ValidatePath`/`ValidateWritePath` and `ValidatePerm`. The path grammar is `fs.ValidPath` and is LEXICAL only — a symlink leaving the tree is the implementation's half (`PathEscaped`). A zero mode is REFUSED, never defaulted (ADR 0031), and bits outside `fs.ModePerm` are refused by name so a typo cannot mint a setuid file; **no registry** (ADR 0056) | `0.2.25.*` |
| `app/view/` | server-side rendering contract: the `Renderer` port FROZEN at two methods (`Render`/`ContentType`), the `Factory` registry, and `TrustedHTML`/`TrustHTML` — the ONE escaping bypass and the only spelling the SDK offers. `Render` returns `[]byte` and never takes an `io.Writer`, because template execution fails halfway and a `ResponseWriter` would already carry the status line, the headers and a plausible prefix of the page. The domain is HTML and has NO `text/template` representation — the two packages are API-compatible, so the substitution compiles and ships stored XSS, and an AST audit fails the build on the import. Both halves of ADR 0031 live in one `Config`: a nil `FS` is REFUSED, a non-positive `MaxBytes` CLAMPS. Six of html/template's seven trust types are refused in render data and have no spelling here at all; the seventh is an ALIAS, so a `Trusted` the caller built wrongly is an XSS the SDK cannot see — stated in four places (ADR 0058) | `0.2.27.*` |
| `data/cache/` | cache contract: the `Store[V]` port FROZEN at three methods (`Fetch`/`Set`/`Delete`) with `EntryFetcher[V]` / `Tagger` / `Loader[V]` as type-asserted siblings, the `EntryValue[V]` a caller stores, the `Fill[V]` FUNC port, and `NoExpiry` as a third TTL meaning distinct from the zero "use the store's default"; **no registry** — and here the mechanics decide it, since `Store` is generic in `V` and Go has no `map[Name]Store[V]` for an open `V`. `Fetch` keeps the primitive's honest verb: a read mutates (ADR 0049, amending ADR 0025) | `0.2.18.*` |
| `app/cli/` | command-line contract: the `Action` and `Binder` FUNC ports (a Binder receives the stdlib's own `*flag.FlagSet`, UNWRAPPED — the ADR 0056 `io/fs` shape), the `Executor` port FROZEN at one method, plus `CommandValue` and `InvocationValue`; **no registry** — one tree per process, so a registry would have exactly one entry. A command is a LEAF or a GROUP, never both (adding a child would silently change what an existing command line means) and never neither (ADR 0031's inert declaration). Nothing in the domain can end the process: `flag.ExitOnError` is refused by name and an AST audit fails the build on `os.Exit`, in production code AND in the suite (ADR 0065) | `0.2.32.*` |
| `security/authz/` | authorization contract: the `Policy` and `Condition` FUNC ports, the three-valued `Decision` (`Abstain` is the ZERO value), the immutable `RequestValue` and the typed `AttrValue`; **no registry** — a registry's key would be a policy name, and there is no such vocabulary that is not the caller's (ADR 0057) | `0.2.26.*` |
| `app/lock/` | mutual-exclusion contract: the `Locker` port FROZEN at two methods (`Acquire`/`TryAcquire`, NEITHER taking a TTL), the `Lease` FROZEN at three (`Fence`/`Extend`/`Release`), and `Deadliner` as the type-asserted sibling a lease implements only when it CAN expire; **no registry** — the two backends differ in exactly that property, so resolving one from a config string would let a typo swap them with every call still succeeding. Ownership is token-checked (a lapsed holder's `Release` releases NOTHING), renewal is refused on a lapsed lease even when nobody has taken the lock, and a fencing token is ISSUED but can only be ENFORCED by the protected resource — stated as a limit, not a footnote (ADR 0052) | `0.2.21.*` |
| `app/health/` | liveness / readiness / startup contract: `Check` (context-carrying) and `SelfCheck` (deliberately context-FREE, so a liveness check cannot reach a dependency), `Probe`, `Status`, `ResultValue`; **no registry** (ADR 0060) | `0.2.29.*` |
| `app/events/` | in-process event bus contract: the `Listener` FUNC port, the frozen three-method `Bus`, `SubscriptionValue` / `DispatchValue` / `Priority`, and the `Halt` control sentinel; keyed on the event's concrete Go TYPE (`EventType = reflect.Type`), so an interface type is refused at registration rather than listed and never called; **no registry**. **`events` is not `queue`** — in-process, synchronous, same-goroutine, no durability and no retry; the frontier table lives in its `CLAUDE.md` (ADR 0053) | `0.2.22.*` |
| `app/statemachine/` | state-machine contract: the `Store[E]` port a machine reads and writes the caller's entities through, FROZEN at five methods where absence is an ANSWER (`found` / `inserted` / `replaced`) and never an error, so `Replace` reporting false is the whole of "never resurrect"; the `Journal[S]` port, frozen at three with variadic `Save`/`Delete`, keeping each entity's `RecordValue` (state, entered-at, `StepValue` history); `Trigger` (start/event/delay/deadline/guard, stable values, `ParseTrigger`) and `CreateEvent`; **no registry** — a store is a caller's collection (ADR 0120) | `0.2.56.*` |
| `security/secret/` | secret contract: the redacting `Value` (every rendering writes `<redacted>`; decodes from a string only; `==` does not compile), the `Store` port FROZEN at five methods over never-reused version numbers, `VersionValue`, and the one DNS-label name grammar every store shares; **no registry** (ADR 0096). Since ADR 0142 also the `SubjectKeyStore` port FROZEN at five — one wrapped data key per subject, `Insert` insert-if-absent and `Replace` compare-and-swap across processes, absence an answer — `SubjectKeyValue`, and the lowercase subject-reference grammar `ValidateSubject` | `0.2.37.*` |
| `security/session/` | server-side session contract: a `Store` port FROZEN at five methods with `Sweeper` as its first type-asserted sibling, the `Sealer` that renders an identifier as a cookie value, the opaque redacting `ID`, and the immutable `SessionValue`; **no registry**. `Regenerate` is the only call that binds a subject and always rotates the identifier, so session fixation is prevented by absence rather than by a step (ADR 0045) | `0.2.14.*` |
| `data/sql/` | relational-database ports above `database/sql`: `Executor` (frozen at three methods, none of which ends a transaction) + its ADR 0039 sibling `Preparer`, `Transactor` + its two ADR 0039 siblings `Joiner` (the executor of the transaction a context carries, else the pool) and `Deferrer` (a function held until that transaction commits — ADR 0139), `Checker`, `Migrator`, plus `TxOptionsValue` / `MigrationValue` / `Dialect` and the `TxFunc` / `Step` FUNC ports. The ports speak the stdlib's own `*sql.Rows` / `*sql.Row` / `sql.Result` and never re-declare them (ADR 0055 §D1); `Dialect` is a CLOSED set with two distinct refusals, which spells its engine's vocabulary — `Placeholder`, `QuoteIdent`, `ForUpdate`, `ForUpdateSkipLocked` — for every statement `service/data/sql`, `docstore` and `queue` render, and never a statement itself; **no registry** (ADR 0055) | `0.2.24.*` |
| `observe/trace/` | distributed-tracing contract: the `Tracer` (1 method) / `Span` (5 methods) ports, both FROZEN, the immutable `SpanContextValue` that travels between processes, the W3C **Trace Context** `traceparent`/`tracestate` format implemented from the ABNF, the OTel span model (`SpanKind` / `StatusValue` / `EventValue` / `LinkValue` / `SpanValue` / `SpansValue`), the `Sampler` + `SpanSink` FUNC ports, the two-method `Carrier` that `http.Header` satisfies with no adapter, and the `SpanExporter` registry. Attributes, `ResourceValue` and `ScopeValue` are `core/observe/otel`'s, used directly rather than redeclared — they are `common.proto`/`resource.proto`, shared by every signal — while `DefaultScopeName` and the `InvalidAttribute` refusal (`0.2.20.7`) deliberately are not. The sampling decision is taken ONCE, at the root, and travels in the `sampled` bit (ADR 0051) | `0.2.20.*` |

The distribution domains — `entitlement`, `selfupdate`, `gate` and `vcs` (now
`git`) — left this layer for the framework (ADR 0158): their contracts are
`framework/internal/core/<domain>`, and their ranges `0.2.33.*`–`0.2.36.*` kept
their values there (ADR 0160).

`Major=0` (internal), `Layer=2` (core). Each package owns the slot in the last column of the table; `docs/error-codes.yaml` lists every code they declare. `observe/logger/` is the one package whose slot is reserved and holds no code.

## Module

Single Go module `github.com/kitsunium/sdk/internal/core` — one `go.mod`, one `go.sum`. `replace` resolves `internal/kernel` to `../kernel`.

## Conventions

- **Interface-first.** Core packages expose interfaces + immutable value structs. Any method with a non-trivial body belongs in `internal/service/*`.
- **Role-suffix on exported structs** (ktn-linter `KTN-STRUCT-ROLE`): `AttrValue`, `RecordEvent`, `Value`. Short aliases (`Attr = AttrValue`) re-exported at `pkg/v1/observe/logger`.
- **Imports allowed**: stdlib + `internal/kernel/*` + **other `internal/core/*` packages**. Never `internal/service/*`, never `pkg/*`, never `third-party/`. A lateral import is not a violation: `scripts/check-layer-deps.sh`'s core query names what is above core and says nothing about siblings, and `core/observe/metrics` and `core/observe/trace` both build on `core/observe/otel`'s shared model by decision (ADR 0051 §2). This line used to omit the sibling case, the gap `internal/CLAUDE.md` records for the service row.
- **Plug-in registries** (codec, writer): constructors carry `// IFACE-PLUGIN:` markers — concrete types stay unexported; the registry hands instances back behind the domain interface (`Codec`, `Factory`).

## Do NOT

- Add concrete runtime types with stateful methods here. The `codec` / `writer` / `crypto` / `transform` / `id` / `metrics` / `trace` / `view` registries' `snapshot.Value`-backed lookups are the deliberate exceptions — they carry no domain logic, only routing.
- Import `context` outside of interface signatures — **except for a single-method function port**, i.e. a named `func(ctx context.Context) …` type that IS the contract (`resilience.Operation`, ADR 0026). Such a type is a declaration, not plumbing: it is the function-shaped equivalent of a one-method interface, and the Go stdlib uses the same shape (`http.HandlerFunc`). Forcing it into an interface would make every call site write an adapter for no gain. This exception does NOT admit `context` in struct fields, value types, or package-level state.
- Reach upward into `internal/service/*` or `pkg/*`.
- Grow a **new** sibling without first widening the layer's purpose statement (the `writer` sibling was admitted by ADR 0012; `crypto` by ADR 0013; `transform` by ADR 0014; `proc` by ADR 0016; `id` by ADR 0024, which opens the Phase-B new-domain wave — observability/reliability/configuration land in ADR 0025–0028; `net` by ADR 0029; `scheduler` by ADR 0041; `token` by ADR 0042; `session` by ADR 0045; `validation` by ADR 0046, which also records how it FEEDS `config.Validator` instead of replacing it; `cache` by ADR 0049, which adds a domain ABOVE the ADR 0025 kernel primitive rather than moving it; `lifecycle` by ADR 0050, which assembles the pieces the other domains already ship rather than adding a new capability; `trace` by ADR 0051, which completes observability's third pillar and REUSES this layer's attribute model rather than twinning it; `lock` by ADR 0052; `events` by ADR 0053, which admits an IN-PROCESS bus and draws the line to the `queue` domain that follows before either can drift into the other).

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

- `data/codec/` — see `internal/core/data/codec/CLAUDE.md`
- `observe/logger/writer/` — see `internal/core/observe/logger/writer/CLAUDE.md`
- `crypto/` — see `internal/core/crypto/CLAUDE.md`
- `crypto/key/jwk/` — see `internal/core/crypto/key/jwk/CLAUDE.md` (the JWK format's codes, declared at the path that mirrors the service — ADR 0160)
- `data/transform/` — see `internal/core/data/transform/CLAUDE.md`
- `proc/` — see `internal/core/proc/CLAUDE.md` (OS process-supervision foundation, ADR 0016)
- `proc/ipc/` — see `internal/core/proc/ipc/CLAUDE.md` (the private socket's ports and values, and why the configuration is not among them — ADR 0160)
- `data/queue/` — see `internal/core/data/queue/CLAUDE.md` (the frozen Broker, at-least-once in the type, and why the routing key is a name — ADR 0054; the siblings and the mark ADR 0151 added)
- `app/id/` — see `internal/core/app/id/CLAUDE.md` (identifier generation, ADR 0024)
- `app/i18n/` — see `internal/core/app/i18n/CLAUDE.md` (the frozen Catalog, the CLDR Form, and why the plural rules follow the message — ADR 0063)
- `app/resilience/` — see `internal/core/app/resilience/CLAUDE.md` (reliability policies, ADR 0026)
- `observe/metrics/` — see `internal/core/observe/metrics/CLAUDE.md` (observability, ADR 0027 / ADR 0044)
- `observe/otel/` — see `internal/core/observe/otel/CLAUDE.md` (the model both signals share, and why the refusal is the caller's — ADR 0051 §Decision 2)
- `app/config/` — see `internal/core/app/config/CLAUDE.md` (configuration + schema, ADR 0028 + ADR 0061)
- `net/` — see `internal/core/net/CLAUDE.md` (network domain, ADR 0029)
- `app/scheduler/` — see `internal/core/app/scheduler/CLAUDE.md` (time-driven execution, ADR 0041)
- `app/lifecycle/` — see `internal/core/app/lifecycle/CLAUDE.md` (ordered start/stop, ADR 0050 — and why there is deliberately no dependency graph and no autowiring)
- `observe/` — see `internal/core/observe/CLAUDE.md` (the observe family: its members, the rule that put them together, and why the writer registry sits beneath the logger)
- `security/` — see `internal/core/security/CLAUDE.md` (the security family: its members, the rule that put them together, and why a scheme they call stays in `crypto/`)
- `data/` — see `internal/core/data/CLAUDE.md` (the data family: its members, the rule that put them together, and why `docstore` has no contract here yet)
- `app/` — see `internal/core/app/CLAUDE.md` (the app family: its members, the rule that put them together, and why the in-process `events` bus is here while the durable `queue` is `data`'s)
- `security/token/` — see `internal/core/security/token/CLAUDE.md` (security tokens, ADR 0042)
- `app/validation/` — see `internal/core/app/validation/CLAUDE.md` (value validation, ADR 0046)
- `data/vfs/` — see `internal/core/data/vfs/CLAUDE.md` (the filesystem port, ADR 0056 — and why reading is an alias rather than a new interface)
- `app/view/` — see `internal/core/app/view/CLAUDE.md` (server-side rendering, ADR 0058 — the contract built ON html/template, the one trust type, and the XSS the SDK cannot see)
- `app/lock/` — see `internal/core/app/lock/CLAUDE.md` (mutual exclusion, ADR 0052 — ownership, renewal and fencing decided out loud, and the guarantee the domain does NOT make)
- `data/cache/` — see `internal/core/data/cache/CLAUDE.md` (the cache domain, ADR 0049 — and why it is NOT `internal/kernel/collections/cache`, which stays exactly what ADR 0025 made it)
- `app/cli/` — see `internal/core/app/cli/CLAUDE.md` (the FUNC ports, leaf-XOR-group, and why nothing can exit — ADR 0065)
- `security/authz/` — see `internal/core/security/authz/CLAUDE.md` (the two FUNC ports, why abstention is the zero value — ADR 0057)
- `observe/trace/` — see `internal/core/observe/trace/CLAUDE.md` (distributed tracing, ADR 0051 — the W3C refusals, and why the attribute model is `core/observe/otel`'s)
- `app/statemachine/` — see `internal/core/app/statemachine/CLAUDE.md` (the two ports the caller implements, why absence is an answer, and why a trigger is stored as a number — ADR 0120)
- `security/secret/` — see `internal/core/security/secret/CLAUDE.md` (the redacting value, the frozen versioned port, and why the bytes sit behind a pointer — ADR 0096; the subject-key port and why a subject is lowercase — ADR 0142)
- `security/session/` — see `internal/core/security/session/CLAUDE.md` (server-side sessions, ADR 0045 — and the frontier table that keeps this domain from becoming an authentication framework)
- `data/sql/` — see `internal/core/data/sql/CLAUDE.md` (the four ports and their siblings, why they speak the stdlib's own types, and the closed dialect set — ADR 0055, ADR 0139)
- `app/events/` — see `internal/core/app/events/CLAUDE.md` (in-process event bus, ADR 0053 — and the `events` / `queue` frontier table, which is the first thing to read there)
- `observe/logger/` — see `internal/core/observe/logger/CLAUDE.md` (the human-readable surface doc is the facade's generated `pkg/v1/observe/logger/README.md`)
- `app/mail/` — see `internal/core/app/mail/CLAUDE.md` (the frozen Transport, Bcc in the envelope, and the injection gate — ADR 0064)
- `observe/logger/level/` — see `internal/core/observe/logger/level/CLAUDE.md`
- `app/health/` — see `internal/core/app/health/CLAUDE.md` (startup, readiness and liveness as three types, ADR 0060)
- `data/codec/scratch/` — see `internal/core/data/codec/scratch/CLAUDE.md`
