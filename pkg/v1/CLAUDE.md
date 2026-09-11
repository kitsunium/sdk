<!-- updated: 2026-05-18T14:30:00Z -->
# pkg/v1/

## Purpose

The first major version of the SDK's public API. Type signatures exposed here are **frozen post-v1.0.0** — breaking changes land in `pkg/v2`. Today the surface is a thin alias + helper layer over `internal/core/*` and `internal/service/*` (zero runtime cost; the Go type system treats `pkg/v1/X.T` and `internal/.../T` as the same type).

## Contents

| Package | Role | README |
|---|---|---|
| `logger/slogbridge/` | `NewHandler` / `New` — adapt an SDK `Logger` to `log/slog` for APIs typed on the concrete `*slog.Logger` (ADR 0032). The one package allowed to import `log/slog` | `pkg/v1/logger/slogbridge/README.md` |
| `logger/` | Logger facade: `Config` / `NewText` / `Default` / `NewWithSink`, `Info|Warn|Error|Debug`, `Build` builder, `String|Int|…` attr ctors, `Version` (ldflags injection point), `TraceContext` / `TraceContextFromContext` — the ONE place logging and tracing meet, wired into every constructor so `trace_id` / `span_id` land on every record emitted inside a span (ADR 0062) | `pkg/v1/logger/README.md` |
| `mail/` | Compose and send mail — ADR 0064; header injection is refused rather than sanitised, Bcc reaches the envelope and never a header, and `TLSMode`'s zero value is refused because neither "encrypt" nor "do not" is a safe guess | `pkg/v1/mail/README.md` |
| `codec/` | Universal codec dispatch: `Marshal` / `Unmarshal` / `NewEncoder` / `NewDecoder` over a `Format` registry; blank-imports 16 service codecs covering 24 Format names — text/binary/base-N reached identically (asn1-der, baseenc family [base64/base64url/base32/base16/hex/ascii85/base45/base58/base62], bson, cbor, csv, flatbuffers, form, json, msgpack, multipart, ndjson, pem, tlv, toml, xml, yaml) | _(no README)_ |
| `errs/` | Error introspection **and construction** (ADR 0019): read — `CodeOf` / `ReasonOf` / `PublicOf` / `PrivateOf` / `HTTPStatusOf` / `ExitCodeOf` / `HasCode` / `HasReason` / `NewPrefixMatcher`; build — `New` / `Wrap` (+ `WrapParams`) / `Field` helpers (`String` / `Int` / `Int64` / `Bool` / `Float` / `NewFieldValue`); codes — `Pack` / `ParseCode` / `MinAppMajor` / `MaxMajor` + `Code` / `Major` / `Layer` / `PkgCode` / `Serial` / `Field` / `PrefixMatcher` type aliases + `MaskBy*` constants. Octets are composable on the typed `Code` (e.g. `code.Layer()`). | `pkg/v1/errs/README.md` |
| `id/` | Identifier generation (ADR 0024): `New(scheme)` dispatch + helpers `UUIDv4` / `UUIDv7` / `ULID` / `Snowflake` / `NanoID` / `KSUID`; configured constructors `NewSnowflake(node)` / `NewNanoID(size)` / `NewTypeID(prefix)`; decoders `ParseKSUID` / `ParseTypeID` / `FormatTypeID`; `Available`; `Scheme` constants; stdlib-only, cross-OS | `pkg/v1/id/README.md` |
| `i18n/` | Translated messages with CLDR plural forms that are right in Polish and Arabic — ADR 0063; an unsupported language and an incomplete translation are startup failures, and `Accept-Language` negotiation never fails a request | `pkg/v1/i18n/README.md` |
| `cache/` | **Two surfaces, two jobs.** The PRIMITIVE (ADR 0025): generic LRU + TTL `Cache[K,V]` — `New` + `Fetch` / `Set` / `SetTTL` / `Delete` / `Len` / `Purge` / `Stats`, aliases onto `kernel/cache`. The DOMAIN (ADR 0049): `NewMemory` / `NewChain` → a `Store[V]` frozen at three methods, with `EntryFetcher` / `Tagger` / `Loader` reached by type assertion (ADR 0039), `Entry[V]` and `NoExpiry`, and the sentinels. `Fetch` on both — a hit mutates, so a read is not free. `MaxEntries` is REFUSED at zero, never defaulted (ADR 0031). Tag invalidation is O(k) in the tagged entries, not O(N). **`Loader.Load` collapses concurrent misses within ONE process — it does not coordinate across replicas, so size an origin against the replica count, not against one.** Nothing across chained tiers is atomic and the package says which side of each window it protects. Stdlib-only, cross-OS | `pkg/v1/cache/README.md` |
| `authz/` | Authorization (ADR 0057): `NewRBAC` / `NewABAC` / `DenyOverrides` / `Check`, the `Attr*` constructors and conditions, `Not`/`AllOf`/`AnyOf`, `Must`/`MustCondition`. **Three decisions, not two** — `Allow` / `Deny` / `Abstain`, and `Abstain` is the zero value, so a policy that forgets to answer grants nothing. A request no policy is applicable to is refused. Deny-overrides is the only combiner. Every refusal renders one sentence that names no policy, role or attribute; the diagnosis is in the fields. **No policy DSL, no wildcard, no relationship model** — a condition is a Go func. stdlib-only | `pkg/v1/authz/README.md` |
| `lock/` | Mutual exclusion (ADR 0052): `NewMemory` / `NewFileLocker` → a `Locker` you `Acquire` / `TryAcquire`, returning a `Lease` with `Fence` / `Extend` / `Release`; `Keepalive` renews in the background and CANCELS a derived context when the lease is lost, because the caller who needs that news is already inside the section. **A zero TTL is REFUSED, never defaulted** — its two readings are opposites and one grants every `Acquire` while excluding nobody (ADR 0031). `Release` releases only a lock this holder STILL holds; a lapsed lease releases nothing and reports `LOCK_NOT_HELD` rather than ending the new holder's section. **The `Deadliner` type assertion is the question that changes how a caller is written**: the memory lease can be taken from you and answers it, the file lease cannot and deliberately does not. **The SDK ISSUES a fencing token; only the protected resource can ENFORCE one — without that check, mutual exclusion is not guaranteed against a GC pause or a `SIGSTOP`.** Scope is one process or one machine; distributed backends belong under `third-party/`. Stdlib-only; the file locker refuses `UnsupportedPlatform` where `flock(2)` does not exist | `pkg/v1/lock/README.md` |
| `health/` | Startup / readiness / liveness probes — ADR 0060; the liveness registration takes a context-free func so the classic mis-wiring does not compile |
| `events/` | In-process event bus (ADR 0053): `New` → a `Bus`, `On[E]` / `Off[E]` to register and remove a typed `Handler[E]`, `Bus.Publish` to dispatch. **Not a queue** — synchronous, same-goroutine, same transaction, no durability, no retry, no dead-letter path, and no async mode; the frontier table is the first thing the README renders. Listeners run in ascending `Priority` with ties in registration order, both promises. `Halt` stops the remaining listeners and only a `MayHalt: true` handler may return it; a halt is not a failure. Listener errors never short-circuit and aggregate with `errors.Join`, `ListenerFailed` beside the cause; a listener panic is recovered with its originating stack and does not take the publisher or the siblings down. An interface event type is refused at registration, because dispatch routes on a value's dynamic type. stdlib-only, cross-OS | `pkg/v1/events/README.md` |
| `resilience/` | Reliability policies (ADR 0026): `NewRetry` / `NewCircuitBreaker` / `NewRateLimiter` / `NewBulkhead` / `NewTimeout` / `NewFallback` / `NewHedge` returning composable `Runner`s; `Operation`/`Runner` + `*Config` aliases; outcome sentinels; stdlib-only, cross-OS | `pkg/v1/resilience/README.md` |
| `metrics/` | Observability on the OpenTelemetry data model, implemented from the spec with zero OTel imports (ADR 0027, ADR 0044, ADR 0048): `NewMeter` → `FullMeter` (lock-free Counter/UpDownCounter/Gauge/Histogram + `Observable*` callbacks); typed attributes via `String`/`Bool`/`Int64`/`Float64`; `Temporality`, `Resource`, `Scope`; `Collect` → `Snapshot` (name → metric → series); `Export`/`RegisterExporter`/`NewTextExporter`/`NewPrometheusExporter`/`AvailableExporters`; the `text`, `prometheus` and `otlpjson` exporters all register on **stderr** (ADR 0030 — stdout may be the process's protocol channel), and the OTLP/HTTP emitter registers nowhere at all, stdout reachable via `NewTextExporter(name, os.Stdout)` and a scrape via `NewPrometheusExporter(name, w)`; `prometheus` is a deliberately LOSSY connector — it refuses a delta snapshot and flattens every attribute to a string, while `otlpjson` is the native OTLP wire and loses nothing (ADR 0048): `EncodeOTLPJSON` for the bytes alone, `NewOTLPJSONExporter` for a stream, `NewOTLPHTTPExporter`/`OTLPHTTPConfig`/`OTLPRetryable` for the POST — never registered, and it classifies rather than retrying so `resilience` owns the backoff; stdlib-only, cross-OS | `pkg/v1/metrics/README.md` |
| `queue/` | Asynchronous durable message queue (ADR 0054): `NewFile` → the durable broker (its state is a directory; it survives the process), `NewMemory` → the test double, `Consume` to run a `Handler` on goroutines it owns. **Not an event bus** — many processes, asynchronous, a consumer's goroutine, durable, retried, dead-lettered. Delivery is AT-LEAST-ONCE, stated three times because a reader who assumes otherwise double-charges a card: `Delivery.Deliveries` counts from 1, `ConsumerConfig.HandlerIsIdempotent` must be true, and a message is removed at acknowledgement and never at read. Ordering is not guaranteed and a single retry loses it. `DeadLetterReader` and `LeaseExtender` are ADR 0039 siblings reached by type assertion. `Policy` refuses `VisibilityTimeout`/`MaxDeliveries` at zero and clamps `RetryDelay`/`MaxMessageBytes`. stdlib-only; `NewFile` refuses at construction off its native platforms | `pkg/v1/queue/README.md` |
| `trace/` | Distributed tracing on the OpenTelemetry trace data model + W3C Trace Context, implemented from the specifications with zero OTel imports (ADR 0051): `NewTracer` → `Tracer.Start` (a frozen 1-method port returning a frozen 5-method `Span`), `NewRecorder` → `Sink`/`Collect`/`Dropped`, `Inject`/`Extract` over a two-method `Carrier` that `http.Header` satisfies with no adapter, `ServerMiddleware`/`ClientMiddleware` as the network domain's own middleware type, `RecordError`, and the OTLP wire — `EncodeOTLPJSON` for the bytes, `NewOTLPJSONExporter` for a stream, `NewOTLPHTTPExporter`/`OTLPRetryable` for the POST to `/v1/traces` (never registered, and it classifies rather than retrying). **`trace.Attr` IS `metrics.Attr`** — one type, because an attribute is OTel's shared `common.proto`. Sampling is decided ONCE at the root and travels in the `sampled` bit; `Ratio` REFUSES a rate of 0, which also spells "unconfigured" (ADR 0031). `otlpjson` registers on **stderr** (ADR 0030). Stdlib-only, cross-OS | `pkg/v1/trace/README.md` |
| `config/` | Configuration (ADR 0028 + ADR 0061), and the schema half first: `NewSchema`/`LoadSchema` plus `Schema`/`SchemaSpec`/`Default` — a missing required key fails at STARTUP with every missing key named at once, an unknown key is refused by default (`AllowUnknownKeys` opts out), a default is a layer merged UNDER every source so `port = 0` stays 0, and a key is never both required and defaulted. No message ever repeats a value. `SchemaSpec.Rule` IS a `validation.Constraint` — this domain composes `pkg/v1/validation` rather than doubling it. Then the original: generic `Load[T]` generic `Load[T]` merging `EnvSource`/`FileSource` (later wins) + decode + `Validator`; `PollWatcher` cross-OS hot-reload; stdlib-only | `pkg/v1/config/README.md` |
| `scheduler/` | Time-driven execution (ADR 0041): `Parse`/`ParseInLocation` (five-field POSIX cron, UTC by default) + `Every` + `New` → a `Scheduler` you `Add` to and `Run`; `Job`/`Schedule`/`Entry`/`Result`/`Config` aliases; every construct outside the subset refused BY NAME at construction; DST, missed deadlines and overlap documented rather than emergent; stdlib-only, cross-OS | `pkg/v1/scheduler/README.md` |
| `lifecycle/` | Ordered start/stop (ADR 0050): `New` → a `Lifecycle` you `Add` components to, plus `Run` for the whole of a service main. `Start`/`Stop`/`Component`/`Transition`/`Phase`/`Signal`/`Config`/`RunConfig` aliases. The Add order IS the dependency order and shutdown is its exact reverse — no graph, no autowiring, both recorded as decisions. **A partial start is unwound before `Start` returns**, through the same path an ordinary `Stop` uses, with contexts detached from the cancellation that may have caused it; the component that failed is not stopped. **`StopTimeout` is the budget ONE component gets**, so a component that will not finish is abandoned at its own deadline and the rest keep theirs; expiry cancels that `Stop`'s context and stops waiting — it kills no goroutine and closes nothing the component owns. A non-positive budget clamps to `DefaultStopTimeout` (30s). Signals and `sd_notify` are opt-in `RunConfig` fields; the zero value wires nothing. Errors are `errors.Join` aggregates, so `errs.HasCode` and `errors.Is` both answer. stdlib-only, cross-OS | `pkg/v1/lifecycle/README.md` |
| `session/` | Server-side sessions (ADR 0045): `NewMemoryStore` / `NewFileStore` → a `Store` you `New` / `Load` / `Save` / `Regenerate` / `Destroy`; `NewSealer` produces the cookie's VALUE and nothing writes a cookie. `Regenerate` is the ONLY call that binds a subject and always mints a new identifier, so session fixation is not a step anyone can forget. `ID` redacts itself, compares with `crypto/subtle`, and is logged as `Digest()` — never `Reveal()`. Expiry is absolute AND sliding, the earlier deadline always wins, and a zero timeout is refused. The file store refuses with `UnsupportedPlatform` where its OS mechanics do not exist rather than pretending. Unlike `token/`, revocation is immediate. stdlib-only | `pkg/v1/session/README.md` |
| `sql/` | Relational database over `database/sql` (ADR 0055), and deliberately **not** an ORM: `NewTransactor` / `NewChecker` / `NewMigrator` plus the `Executor` / `Preparer` / `Transactor` / `Checker` / `Migrator` / `TxOptions` / `Migration` / `Step` / `Dialect` / `Config` / `PoolConfig` / `MigrateConfig` aliases, `ParseDialect`, `Statements`, `Irreversible` and the ergonomic `Transact`. **Ships no driver** — import the one you want, open the `*sql.DB` yourself, hand it over. A unit of work receives an `Executor` with no Commit and no Rollback, so it cannot end the transaction it was lent; a nested `Transact` is a savepoint, and non-zero options on one are refused. `MaxOpen` is required (database/sql reads 0 as unlimited). Migrations are VALUES you build — no directory, no file format, no naming convention — applied under the engine's own advisory lock, which dies with the connection that holds it. Every failure is a typed SDK error joined with the driver's own, and no `Public` ever carries a connection string, a query or a bound argument | `pkg/v1/sql/README.md` |
| `token/` | Security tokens (ADR 0042): JWT over JWS Compact Serialization + PASETO v4.public. One constructor per algorithm — `NewHS256Verifier` takes a `crypto.Key`, `NewES256Verifier` an `*ecdsa.PublicKey` — so algorithm confusion is a call that does not compile; `alg:none` has no representation in `Algorithm`; `exp` is required unless opted out by name; `NewSetVerifier` selects by `kid` from a JWK Set and resolves a duplicated one by signature. `Claims` prints its shape, never its values. stdlib-only | `pkg/v1/token/README.md` |
| `validation/` | Value validation (ADR 0046): `Constraint`/`Violation`/`Report` aliases + `All`/`First`/`Field`/`Each`/`Check`/`Must`, the built-ins (`Required`/`AtLeast`/`AtMost`/`Between`/`Length`/`Count`/`OneOf`/`Matches`) and `Struct[T]` — the struct-tag front end whose plan is cached per type. A violation says WHERE in one path grammar (`JoinField`/`JoinIndex`, json-tag member names); every violation is collected by default; neither `Violation` nor `Report` is an `error` (`Report.Err()` converts and returns a genuine nil); a message never echoes the value. Feeds `config.Validator` without replacing it. stdlib-only | `pkg/v1/validation/README.md` |
| `vfs/` | Filesystem (ADR 0056): `NewOS` → a tree-confined filesystem, `NewMem` → a double that needs no temporary directory. `FS`/`WritableFS`/`AtomicWriter`/`FullFS` aliases plus the ten sentinels. Reading is `io/fs` unchanged, so `fs.WalkDir`, `fs.Glob` and `fs.ReadFile` work with no adapter. `WriteAtomic` publishes via a temporary + `rename(2)` + two flushes: on failure the previous bytes are byte-for-byte intact and no temporary survives. A refusal carries BOTH identities — `errors.Is(err, vfs.ReadFailed)` and `errors.Is(err, fs.ErrNotExist)` both answer — so the package can be adopted one call at a time. A zero mode is refused, never defaulted. stdlib-only; `NewOS` refuses at construction off its native platforms | `pkg/v1/vfs/README.md` |
| `view/` | Server-side rendering (ADR 0058): `New(Config{FS: …})` → a `Renderer` you `Render(ctx, name, data)`, returning the complete document or nothing — it never takes an `io.Writer`, because a mid-execution failure would already have flushed the status line and half the page. The engine is the stdlib `html/template`, USED and not reimplemented: it is the only Go engine that escapes according to CONTEXT, and a hand-written escaper would be the security regression this domain exists to prevent. `text/template` has no representation at all and an AST audit fails the build on the import. **`TrustHTML` is the one bypass and the only spelling the SDK offers**; the other six html/template trust types are REFUSED in render data with the path named. **What it does NOT prevent is stated out loud: `TrustedHTML` is a type alias, so a `Trusted` the caller built wrongly is an XSS the SDK cannot see** — the domain prevents an accidental bypass and gives the deliberate one one greppable word. **Parse once**: reparsing per request costs 34× on a realistic tree. Stdlib-only, cross-OS | `pkg/v1/view/README.md` |

The `codec/` sub-package was added since the original CLAUDE.md. The legacy `codec/baseenc/` byte-level package was removed in favour of uniform `codec.Marshal("base64"|"base64url"|"base32"|"base16"|"hex"|"ascii85", v)` dispatch — every encoding format now goes through the same verb.

## Module

Single Go module `github.com/kitsunium/sdk/pkg` — one `go.mod` (at `pkg/go.mod`), one `go.sum`. The consumer packages live under this `v1/` directory, so import paths stay `github.com/kitsunium/sdk/pkg/v1/*`; only the module declaration sits one level up. Go forbids a `/v1` module-path suffix, so the module itself is the bare `…/pkg` (ADR 0017). Built and tested under Bazel via `//pkg/v1/...`; `cd pkg && GOWORK=off go test ./...` works for local iteration.

## Public surface contract

- **Stable identifiers.** Every exported name / type / const in `pkg/v1/*` is frozen until `pkg/v2` cuts. New helpers can be added; existing signatures cannot move.
- **Aliases, not new types.** Public types are `type X = internalPkg.X` so consumers and SDK code share the type identity (a `pkg/v1/logger.Attr` passes anywhere `corelogger.AttrValue` is expected).
- **Error model is constructable; other internal types are not.** Since ADR 0019, `pkg/v1/errs` exposes `New` / `Wrap` (+ `WrapParams`) and the `Field` helpers alongside the `Of`-accessors. Consumers receive `error`, introspect it, AND mint their own typed errors in the same model — but the concrete `*errs.Error` stays unexported, so they cannot forge one by struct literal; construction routes through the runtime-validated constructors (which return a typed `CodeInvalid*` error, never panic). The kernel `errs.Define` (panic-at-init, AST-audited) stays internal; the public path is the non-panicking `New`/`Wrap`. This exception is `errs`-only — logger/codec internals keep their constructors private.
- **`FieldValue` is passed through** (as the `Field` alias) since ADR 0019, so consumers attach structured metadata at construction. The previously-deferred `FieldsOfAsMap(err) map[string]string` read-side helper is still add-on-demand.

## Version freeze policy

- `pkg/v1` signatures freeze on first `v1.0.0` tag. Breaking signature changes after the freeze go to `pkg/v2` (coexists with v1 until deprecation).
- **Pre-1.0.0 precedent for breaking changes.** Two waves of breaking changes have already landed under `pkg/v1`:
  1. ADR 0002 errors-package MR — 4 breaking signature changes + 1 behaviour change (e.g. `NewText(Config{Writer: nil})` now returns `WriterRequired` instead of silently defaulting to stderr).
  2. PR #25 (`refactor!: drive ktn-linter phases 1-7 to zero issues`) — `baseenc.Encoding` migrated from untyped `string` constants to a typed `int` + `iota` block with `EncodingUnknown` as the zero-value sentinel. Caller code that compared `Encoding` to a string literal stopped compiling; the typed form makes typos catchable at the call site.
- Both waves shipped before `v1.0.0`. **After v1.0.0 the policy hardens: no breaking changes in `pkg/v1`** — any further migrations go to `pkg/v2`. The pre-1.0 precedent does not authorise post-1.0 breakage.
- Security fixes in `internal/*` propagate via minor bumps on the affected module without touching `pkg/v1` — the facade re-exports, it does not duplicate.

## Conventions

- Import aliases at call sites: `logger "…/pkg/v1/logger"`, `errs "…/pkg/v1/errs"`, `codec "…/pkg/v1/codec"`.
- `import _ "github.com/kitsunium/sdk/pkg/v1/codec"` is enough to activate all 16 service codecs (24 Format names incl. base-N family) — registry side-effects driven by blank imports.
- Every emitted log record carries `framework_version` via the ldflags-injected `logger.Version`. Injection recipe is in `pkg/v1/logger/README.md`; under Bazel `--stamp` + `x_defs` + `tools/workspace_status.sh` (`STABLE_VERSION`) supply the same value.
- `logger.NewText(Config{Writer: nil})` returns `(nil, WriterRequired)`. `logger.NewWithSink(SinkConfig{Sink: nil})` returns `(nil, SinkConfigRequired)`. Use `logger.Default()` for the stderr one-liner.

## Do NOT

- Expose `errs.PrivateOf(err)` output in HTTP / gRPC responses, error pages, or any user-facing surface. Diagnostic-only — documented in the godoc and `pkg/v1/errs/README.md`.
- Build a typed error with raw `errors.New` / `fmt.Errorf` when adopting the SDK error model — use `errs.New` / `errs.Wrap` (ADR 0019) so the result carries a `Code` + Public/Private split. Do NOT reach for the internal `errs.Define` (it panics at init and is `internal/`-blocked); the public `New`/`Wrap` are the runtime-validated path. Assign consumer codes a Major in `[errs.MinAppMajor, errs.MaxMajor]` (`0x40–0x7F`) to stay collision-free with the SDK.
- Set `Version` at runtime from application code — use the ldflags recipe (or Bazel `--stamp`) so every binary commits its version at link time.
- Reach for a byte-level base-N API in `pkg/v1`. There is none — `codec.Marshal("base64", v)` (or stdlib `encoding/base64` directly when you have raw bytes already) is the only path.

## Verification

```
# Primary (Bazel)
bazel test --config=race //pkg/v1/...

# Fallback (per-module; go.mod is at pkg/, code under v1/)
cd pkg
GOWORK=off go test -race -cover ./v1/...
```

## Subtree

- `logger/` — see `pkg/v1/logger/CLAUDE.md`
- `mail/` — see `pkg/v1/mail/CLAUDE.md`
- `logger/slogbridge/` — see `pkg/v1/logger/slogbridge/CLAUDE.md`
- `codec/` — see `pkg/v1/codec/CLAUDE.md`
- `errs/` — see `pkg/v1/errs/CLAUDE.md`
- `events/` — see `pkg/v1/events/CLAUDE.md`
- `id/` — see `pkg/v1/id/CLAUDE.md`
- `i18n/` — see `pkg/v1/i18n/CLAUDE.md`
- `cache/` — see `pkg/v1/cache/CLAUDE.md`
- `authz/` — see `pkg/v1/authz/CLAUDE.md`
- `resilience/` — see `pkg/v1/resilience/CLAUDE.md`
- `metrics/` — see `pkg/v1/metrics/CLAUDE.md`
- `queue/` — see `pkg/v1/queue/CLAUDE.md`
- `trace/` — see `pkg/v1/trace/CLAUDE.md`
- `config/` — see `pkg/v1/config/CLAUDE.md`
- `scheduler/` — see `pkg/v1/scheduler/CLAUDE.md`
- `lifecycle/` — see `pkg/v1/lifecycle/CLAUDE.md`
- `token/` — see `pkg/v1/token/CLAUDE.md`
- `validation/` — see `pkg/v1/validation/CLAUDE.md`
- `vfs/` — see `pkg/v1/vfs/CLAUDE.md`
- `view/` — see `pkg/v1/view/CLAUDE.md`
- `session/` — see `pkg/v1/session/CLAUDE.md`
- `sql/` — see `pkg/v1/sql/CLAUDE.md`

## Before v1 — the shapes this layer publishes

Every `type X = internal…Y` in this tree publishes `Y`'s **shape**, not just its
name. ADR 0039 covers the interface half (extend by a sibling, never by
widening); ADR 0040 covers this half, and gives it an expiry date — a concrete
shape may change while the module is v0, said out loud, and **not after**.

`.claude/contexts/pre-v1-published-shape-audit.md` is the inventory that licence
applies to: **124 concrete structs**, split by what a change to each actually
costs. 43 are construction shapes a caller fills in by field name, where adding
a field is free. The expensive group is the `*Value` shapes the SDK **returns** —
a caller destructures those, so even an added field breaks a composite literal
written without field names.

Review that list before tagging v1, not after. Regenerate it rather than trust
it: the count in it was wrong by a factor of 25 on the first pass, and looked
entirely plausible.
