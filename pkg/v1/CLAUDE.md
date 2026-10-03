<!-- updated: 2026-10-03T06:00:00Z -->
# pkg/v1/

## Purpose

The first major version of the SDK's public API. Type signatures exposed here are **frozen post-v1.0.0** — breaking changes land in `pkg/v2`. Today the surface is a thin alias + helper layer over `internal/core/*` and `internal/service/*` (zero runtime cost; the Go type system treats `pkg/v1/X.T` and `internal/.../T` as the same type).

## Contents

| Package | Role | README |
|---|---|---|
| `observe/logger/slogbridge/` | `NewHandler` / `New` — adapt an SDK `Logger` to `log/slog` for APIs typed on the concrete `*slog.Logger` (ADR 0032). The one package allowed to import `log/slog` | `pkg/v1/observe/logger/slogbridge/README.md` |
| `observe/logger/` | Logger facade: `Config` / `NewText` / `Default` / `NewWithSink` / `Multi` / `LevelGate` (a per-branch floor that applies Info as a floor — ADR 0132), `Info\|Warn\|Error\|Debug`, `Build` builder, `String\|Int\|…` attr ctors, `Version` (ldflags injection point), `TraceContext` / `TraceContextFromContext` — the ONE place logging and tracing meet, wired into every constructor so `trace_id` / `span_id` land on every record emitted inside a span (ADR 0062) | `pkg/v1/observe/logger/README.md` |
| `mail/` | Compose and send mail — ADR 0064; header injection is refused rather than sanitised, Bcc reaches the envelope and never a header, and `TLSMode`'s zero value is refused because neither "encrypt" nor "do not" is a safe guess And the durable outbox (ADR 0111): `NewSpool` → a `Spool` whose `Send` validates, stamps a Message-ID every retry keeps and queues, and whose `Run` delivers with a growing backoff, dead-letters after `MaxAttempts` with the last failure and drops a redelivery of a mail it delivered (a crash between acceptance and acknowledgement is the one resend left, under the same Message-ID); `SendWithID` queues under an identifier the caller minted before the spool had the mail — a dot-atom of at most `SpoolMaxIDBytes`, `InvalidMailID` otherwise — and a repeated one is dropped at delivery once its mail was delivered (ADR 0141); `NewCapture(keep)` is the bounded memory transport. | `pkg/v1/mail/README.md` |
| `data/codec/` | Universal codec dispatch: `Marshal` / `Unmarshal` / `NewEncoder` / `NewDecoder` over a `Format` registry; blank-imports 16 service codecs covering 24 Format names — text/binary/base-N reached identically (asn1-der, baseenc family [base64/base64url/base32/base16/hex/ascii85/base45/base58/base62], bson, cbor, csv, flatbuffers, form, json, msgpack, multipart, ndjson, pem, tlv, toml, xml, yaml) | `pkg/v1/data/codec/README.md` |
| `errs/` | Error introspection **and construction** (ADR 0019): read — `CodeOf` / `ReasonOf` / `PublicOf` / `PrivateOf` / `HTTPStatusOf` / `ExitCodeOf` / `FieldsOf` (a `Field` reads with `Key()` / `StringValue()`) / `HasCode` / `HasReason` / `NewPrefixMatcher`; build — `New` / `Wrap` (+ `WrapParams`) / `Field` helpers (`String` / `Int` / `Int64` / `Bool` / `Float` / `NewFieldValue`); codes — `Pack` / `ParseCode` / `MinAppMajor` / `MaxMajor` + `Code` / `Major` / `Layer` / `PkgCode` / `Serial` / `Field` / `PrefixMatcher` type aliases + `MaskBy*` constants. Octets are composable on the typed `Code` (e.g. `code.Layer()`). | `pkg/v1/errs/README.md` |
| `id/` | Identifier generation (ADR 0024): `New(scheme)` dispatch + helpers `UUIDv4` / `UUIDv7` / `ULID` / `Snowflake` / `NanoID` / `KSUID`; configured constructors `NewSnowflake(node)` / `NewNanoID(size)` / `NewTypeID(prefix)`; decoders `ParseKSUID` / `ParseTypeID` / `FormatTypeID`; `Available`; `Scheme` constants; stdlib-only, cross-OS | `pkg/v1/id/README.md` |
| `i18n/` | Translated messages with CLDR plural forms that are right in Polish and Arabic — ADR 0063; an unsupported language and an incomplete translation are startup failures, and `Accept-Language` negotiation never fails a request | `pkg/v1/i18n/README.md` |
| `clock/` | The time port (ADR 0090): `Clock` (`Now`/`Since`) / `Waiter` (`After`/`NewTimer`/`NewTicker`/`Sleep`) / `Timed` (the union), the `Timer` and `Ticker` handles, `System` and `ManualClock` + `NewManualClock`. **Aliases onto `internal/kernel/clock`, and the alias is the whole point** — Go compares a method signature by TYPE IDENTITY, so before this package a downstream `NewTicker` could not return `clock.Ticker` and `clock.Timed` was unimplementable outside the module. **Nine configurations** take a `Timed` — `scheduler.Config`, `lock.{Memory,File,Keepalive}Config`, `session.FileConfig`, `sql.Config`, `health.Config`, `lifecycle.Config`, `queue.ConsumerConfig` — and could not be driven by a downstream test; fourteen more take a `Clock`, which was assignable but unnameable. `ManualClock` moves only when you move it (`Advance`/`Set`), and `BlockUntil` waits for the code under test to ARM its wait, which is what makes a test deterministic rather than merely fast. The five interfaces are FROZEN (ADR 0039); `System` is a value, not an override hook. stdlib-only, cross-OS | `pkg/v1/clock/README.md` |
| `data/semver/` | Version precedence and Go pseudo-versions (ADR 0156 §4, published by ADR 0159 §4): `IsValid` / `Compare` / `Prerelease` and `IsPseudoVersion` / `PseudoVersionRev` / `PseudoVersionTime` — six forwarders onto `internal/kernel/semver`, the stdlib replacement for `golang.org/x/mod`, with x/mod's names and answers (the two pseudo-version readings return `ok` where x/mod returned an error). An invalid string orders below every version, numbers past `uint64` order exactly, nothing allocates. Published so the framework compares versions without x/mod (ADR 0147) | `pkg/v1/data/semver/README.md` |
| `data/cache/` | **Two surfaces, two jobs.** The PRIMITIVE (ADR 0025): generic LRU + TTL `Cache[K,V]` — `New` + `Fetch` / `Set` / `SetTTL` / `Delete` / `Len` / `Purge` / `Stats`, aliases onto `kernel/collections/cache`. The DOMAIN (ADR 0049): `NewMemory` / `NewChain` → a `Store[V]` frozen at three methods, with `EntryFetcher` / `Tagger` / `Loader` reached by type assertion (ADR 0039), `Entry[V]` and `NoExpiry`, and the sentinels. `Fetch` on both — a hit mutates, so a read is not free. `MaxEntries` is REFUSED at zero, never defaulted (ADR 0031). Tag invalidation is O(k) in the tagged entries, not O(N). **`Loader.Load` collapses concurrent misses within ONE process — it does not coordinate across replicas, so size an origin against the replica count, not against one.** Nothing across chained tiers is atomic and the package says which side of each window it protects. Stdlib-only, cross-OS | `pkg/v1/data/cache/README.md` |
| `cli/` | Command-line applications (ADR 0065): `New` / `Execute` → an `Executor` you hand `os.Args[1:]`, `Command` trees to arbitrary depth, `Invocation`, `Status`, `FlagSource`. Built on the stdlib `flag` — a `Binder` gets the real `*flag.FlagSet`, so every stdlib helper and every `flag.Value` you own works with no adapter. **No cobra, no pflag, no dependency.** **Nothing here can end your process** — `flag.ExitOnError` is refused and an AST audit pins it. `-h` exits **0**; a bad command line writes the same help and exits **64**; a bad tree exits **78**; a command keeps its own `errs` status. `Status(err)` is the nil guard, because `errs.ExitCodeOf(nil)` is 70. `FlagSource` uses `Visit`, so an unset flag never overrides your config file | `pkg/v1/cli/README.md` |
| `security/authz/` | Authorization (ADR 0057): `NewRBAC` / `NewABAC` / `DenyOverrides` / `Check`, the `Attr*` constructors and conditions, `Not`/`AllOf`/`AnyOf`, `Must`/`MustCondition`. **Three decisions, not two** — `Allow` / `Deny` / `Abstain`, and `Abstain` is the zero value, so a policy that forgets to answer grants nothing. A request no policy is applicable to is refused. Deny-overrides is the only combiner. Every refusal renders one sentence that names no policy, role or attribute; the diagnosis is in the fields. **No policy DSL, no wildcard, no relationship model** — a condition is a Go func. stdlib-only | `pkg/v1/security/authz/README.md` |
| `lock/` | Mutual exclusion (ADR 0052): `NewMemory` / `NewFileLocker` → a `Locker` you `Acquire` / `TryAcquire`, returning a `Lease` with `Fence` / `Extend` / `Release`; `Keepalive` renews in the background and CANCELS a derived context when the lease is lost, because the caller who needs that news is already inside the section. **A zero TTL is REFUSED, never defaulted** — its two readings are opposites and one grants every `Acquire` while excluding nobody (ADR 0031). `Release` releases only a lock this holder STILL holds; a lapsed lease releases nothing and reports `LOCK_NOT_HELD` rather than ending the new holder's section. **The `Deadliner` type assertion is the question that changes how a caller is written**: the memory lease can be taken from you and answers it, the file lease cannot and deliberately does not. **The SDK ISSUES a fencing token; only the protected resource can ENFORCE one — without that check, mutual exclusion is not guaranteed against a GC pause or a `SIGSTOP`.** Scope is one process or one machine; distributed backends belong under `third-party/`. Stdlib-only; the file locker refuses `UnsupportedPlatform` where `flock(2)` does not exist | `pkg/v1/lock/README.md` |
| `health/` | Startup / readiness / liveness probes — ADR 0060; the liveness registration takes a context-free func so the classic mis-wiring does not compile; `Ask` is the client half — the question a container's HEALTHCHECK asks, answered ready on 200 alone, with typed reasons otherwise and never a byte of the body (ADR 0131) | `pkg/v1/health/README.md` |
| `events/` | In-process event bus (ADR 0053): `New` → a `Bus`, `On[E]` / `Off[E]` to register and remove a typed `Handler[E]`, `Bus.Publish` to dispatch. **Not a queue** — synchronous, same-goroutine, same transaction, no durability, no retry, no dead-letter path, and no async mode; the frontier table is the first thing the README renders. Listeners run in ascending `Priority` with ties in registration order, both promises. `Halt` stops the remaining listeners and only a `MayHalt: true` handler may return it; a halt is not a failure. Listener errors never short-circuit and aggregate with `errors.Join`, `ListenerFailed` beside the cause; a listener panic is recovered with its originating stack and does not take the publisher or the siblings down. An interface event type is refused at registration, because dispatch routes on a value's dynamic type. stdlib-only, cross-OS | `pkg/v1/events/README.md` |
| `resilience/` | Reliability policies (ADR 0026): `NewRetry` / `NewCircuitBreaker` / `NewRateLimiter` / `NewKeyedRateLimiter` / `NewBulkhead` / `NewTimeout` / `NewFallback` / `NewHedge` returning composable `Runner`s; `Operation`/`Runner` + `*Config` aliases; `Backoff` — the curve `NewRetry` waits, published and never negative; `RetryConfig.Clock` so a `ManualClock` drives a retry; a keyed limiter holds a bucket per `Key(ctx)`, bounded by `MaxKeys` and forgotten after `IdleTimeout`, all four of `Rate`/`Key`/`MaxKeys`/`IdleTimeout` refused at zero (ADR 0103); outcome sentinels; stdlib-only, cross-OS | `pkg/v1/resilience/README.md` |
| `observe/metrics/` | Observability on the OpenTelemetry data model, implemented from the spec with zero OTel imports (ADR 0027, ADR 0044, ADR 0048): `NewMeter` → `FullMeter` (lock-free Counter/UpDownCounter/Gauge/Histogram + `Observable*` callbacks); typed attributes via `String`/`Bool`/`Int64`/`Float64`; `Temporality`, `Resource`, `Scope`; `Collect` → `Snapshot` (name → metric → series); `Export`/`RegisterExporter`/`NewTextExporter`/`NewPrometheusExporter`/`AvailableExporters`; the `text`, `prometheus` and `otlpjson` exporters all register on **stderr** (ADR 0030 — stdout may be the process's protocol channel), and the OTLP/HTTP emitter registers nowhere at all, stdout reachable via `NewTextExporter(name, os.Stdout)` and a scrape via `NewPrometheusExporter(name, w)`; `prometheus` is a deliberately LOSSY connector — it refuses a delta snapshot and flattens every attribute to a string, while `otlpjson` is the native OTLP wire and loses nothing (ADR 0048): `EncodeOTLPJSON` for the bytes alone, `NewOTLPJSONExporter` for a stream, `NewOTLPHTTPExporter`/`OTLPHTTPConfig`/`OTLPRetryable` for the POST — never registered, and it classifies rather than retrying so `resilience` owns the backoff; stdlib-only, cross-OS | `pkg/v1/observe/metrics/README.md` |
| `data/queue/` | Asynchronous durable message queue (ADR 0054): `NewFile` → the durable broker (its state is a directory; it survives the process), `NewSQL` → the durable broker whose state is one table of your own database, every call on the transaction its context carries, so a message published inside your transaction exists if and only if it commits (`SQLMigration` creates the table — ADR 0151), `NewMemory` → the test double, `Consume` to run a `Handler` on goroutines it owns. **Not an event bus** — many processes, asynchronous, a consumer's goroutine, durable, retried, dead-lettered. Delivery is AT-LEAST-ONCE, stated three times because a reader who assumes otherwise double-charges a card: `Delivery.Deliveries` counts from 1, `ConsumerConfig.HandlerIsIdempotent` must be true, and a message is removed at acknowledgement and never at read. Ordering is not guaranteed and a single retry loses it. `DeadLetterReader`, `LeaseExtender`, `Waker`, `Rejecter` and `DeadLetterManager` are ADR 0039 siblings reached by type assertion; `Consume` waits on `Waker`, so an idle consumer sleeps until a publication, a retry falling due or a lapsed lease, and `PollInterval` only bounds another process's publications (ADR 0104); a handler returning `DoNotRetry(err)` is dead-lettered at once through `Rejecter`, and `DeadLetterManager` replays a dead letter with its count reset or deletes it (ADR 0151). `Policy` refuses `VisibilityTimeout`/`MaxDeliveries` at zero, clamps `RetryDelay`/`MaxMessageBytes`, and grows the retry delay up to `MaxRetryDelay` when one is set. stdlib-only; `NewFile` refuses at construction off its native platforms | `pkg/v1/data/queue/README.md` |
| `observe/trace/` | Distributed tracing on the OpenTelemetry trace data model + W3C Trace Context, implemented from the specifications with zero OTel imports (ADR 0051): `NewTracer` → an `SDKTracer`, which is the `Tracer` port's `Start` (a frozen 1-method port returning a frozen 5-method `Span`) plus `Resource`/`Scope`, `NewRecorder` → `Sink`/`Collect`/`Dropped`, `Inject`/`Extract` over a two-method `Carrier` that `http.Header` satisfies with no adapter, `ServerMiddleware`/`ClientMiddleware` as the network domain's own middleware type, `RecordError`, and the OTLP wire — `EncodeOTLPJSON` for the bytes, `NewOTLPJSONExporter` for a stream, `NewOTLPHTTPExporter`/`OTLPRetryable` for the POST to `/v1/traces` (never registered, and it classifies rather than retrying). **`trace.Attr` IS `metrics.Attr`** — one type, because an attribute is OTel's shared `common.proto`. Sampling is decided ONCE at the root and travels in the `sampled` bit; `Ratio` REFUSES a rate of 0, which also spells "unconfigured" (ADR 0031). `otlpjson` registers on **stderr** (ADR 0030). Stdlib-only, cross-OS | `pkg/v1/observe/trace/README.md` |
| `config/` | Configuration (ADR 0028 + ADR 0061), and the schema half first: `NewSchema`/`LoadSchema` plus `Schema`/`SchemaSpec`/`Default` — a missing required key fails at STARTUP with every missing key named at once, an unknown key is refused by default (`AllowUnknownKeys` opts out), a default is a layer merged UNDER every source so `port = 0` stays 0, and a key is never both required and defaulted. No message ever repeats a value. `SchemaSpec.Rule` IS a `validation.Constraint` — this domain composes `pkg/v1/validation` rather than doubling it. Then the original: generic `Load[T]` merging `EnvSource`/`FileSource` (later wins) + decode + `Validator`; `PollWatcher` cross-OS hot-reload; `FSSource` is `FileSource` over an `io/fs.FS`, for a configuration embedded in the binary — a missing file refused on both. And provenance (ADR 0097): `LoadWithOrigins` / `LoadSchemaWithOrigins` return one `Origin{Key, Layer, Detail, Secret}` per leaf key — the layer and the variable or file behind each final value, never the value — through the `Describer` sibling; a `secret.Value` field is marked `Secret` and reads the environment's raw text; stdlib-only | `pkg/v1/config/README.md` |
| `scheduler/` | Time-driven execution (ADR 0041): `Parse`/`ParseInLocation` (five-field POSIX cron, UTC by default) + `Every` + `New` → a `Scheduler` you `Add` to and `Run`; `Job`/`Schedule`/`Entry`/`Result`/`Config` aliases; every construct outside the subset refused BY NAME at construction; DST, missed deadlines and overlap documented rather than emergent; stdlib-only, cross-OS | `pkg/v1/scheduler/README.md` |
| `lifecycle/` | Ordered start/stop (ADR 0050): `New` → a `Lifecycle` you `Add` components to, plus `Run` for the whole of a service main. `Start`/`Stop`/`Component`/`Transition`/`Phase`/`Signal`/`Config`/`RunConfig` aliases. The Add order IS the dependency order and shutdown is its exact reverse — no graph, no autowiring, both recorded as decisions. **A partial start is unwound before `Start` returns**, through the same path an ordinary `Stop` uses, with contexts detached from the cancellation that may have caused it; the component that failed is not stopped. **`StopTimeout` is the budget ONE component gets**, so a component that will not finish is abandoned at its own deadline and the rest keep theirs; expiry cancels that `Stop`'s context and stops waiting — it kills no goroutine and closes nothing the component owns. A non-positive budget clamps to `DefaultStopTimeout` (30s). Signals and `sd_notify` are opt-in `RunConfig` fields; the zero value wires nothing. Errors are `errors.Join` aggregates, so `errs.HasCode` and `errors.Is` both answer. stdlib-only, cross-OS And the supervisor (ADR 0112): `NewSupervisor(name, run, cfg)` restarts a function after every early end on the published backoff, reports every run to `Observe`, joins on `Stop`, and is a `Component`. | `pkg/v1/lifecycle/README.md` |
| `security/secret/` | Secrets (ADR 0096): `Value` (`New` / `FromString`) writes `Redacted` in every rendering — `String`, `GoString`, `Format`, `MarshalJSON`, `MarshalText` — reveals only through `Reveal` / `RevealString`, compares with `Equal` (no `==`), and decodes from a JSON string only, refusing numbers and its own placeholder. `Store` (frozen at five: `Get` / `Versions` / `Put` / `Prune` / `Names`) keeps numbered versions never reused; `NewMemory`, `NewEnv` (read-only, `NAME_FILE`, both set refused), `NewFile` (0700 directory, 0600 records published atomically, writers serialised across processes, sealed when given a key; `UnsupportedPlatform` off Unix). `NewKeyring` — the newest version seals and signs, every kept one opens and verifies, a pruned one is refused. `NewRotator` + `Policy` + `Random` — at least two kept, no goroutine started; `RotatorConfig.InUse` never prunes a version something still needs. `NewSubjectKeys` (ADR 0142) — one data key per subject wrapped by a `Keyring` root: `Seal` / `Open` bound to length-prefixed parts, `Destroy` a cryptographic erase (`KeyDestroyed`), `Rewrap` one small key per subject after a rotation, `OldestRoot` for `InUse`; `SubjectKeyStore` frozen at five and implemented by the caller (`NewMemorySubjectKeyStore` for tests); `ValidateSubject` — a lowercase reference, never an identity. stdlib-only | `pkg/v1/security/secret/README.md` |
| `security/session/` | Server-side sessions (ADR 0045): `NewMemoryStore` / `NewFileStore` → a `Store` you `New` / `Load` / `Save` / `Regenerate` / `Destroy`; `NewSealer` produces the cookie's VALUE and nothing writes a cookie. `Regenerate` is the ONLY call that binds a subject and always mints a new identifier, so session fixation is not a step anyone can forget. `ID` redacts itself, compares with `crypto/subtle`, and is logged as `Digest()` — never `Reveal()`. Expiry is absolute AND sliding, the earlier deadline always wins, and a zero timeout is refused. The file store refuses with `UnsupportedPlatform` where its OS mechanics do not exist rather than pretending. Unlike `token/`, revocation is immediate. stdlib-only | `pkg/v1/security/session/README.md` |
| `data/sql/` | Relational database over `database/sql` (ADR 0055), and deliberately **not** an ORM: `NewTransactor` / `NewChecker` / `NewMigrator` plus the `Executor` / `Preparer` / `Transactor` / `Checker` / `Migrator` / `TxOptions` / `Migration` / `Step` / `Dialect` / `Config` / `PoolConfig` / `MigrateConfig` aliases, `ParseDialect`, `Statements`, `Irreversible` and the ergonomic `Transact`. **Ships no driver** — import the one you want, open the `*sql.DB` yourself, hand it over. A unit of work receives an `Executor` with no Commit and no Rollback, so it cannot end the transaction it was lent; a nested `Transact` is a savepoint, and non-zero options on one are refused. `MaxOpen` is required (database/sql reads 0 as unlimited). Migrations are VALUES you build — no directory, no file format, no naming convention — applied under a lock that dies with its holder: the engine's own advisory lock on PostgreSQL and MySQL, the database file's write lock on SQLite (ADR 0140). The transactor's `Joiner` and `Deferrer` siblings let a callee run in the transaction its context carries and hold a function until that transaction commits (ADR 0139). Every failure is a typed SDK error joined with the driver's own, and no `Public` ever carries a connection string, a query or a bound argument | `pkg/v1/data/sql/README.md` |
| `security/token/` | Security tokens (ADR 0042): JWT over JWS Compact Serialization + PASETO v4.public. One constructor per algorithm — `NewHS256Verifier` takes a `crypto.Key`, `NewES256Verifier` an `*ecdsa.PublicKey` — so algorithm confusion is a call that does not compile; `alg:none` has no representation in `Algorithm`; `exp` is required unless opted out by name; `NewSetVerifier` selects by `kid` from a JWK Set and resolves a duplicated one by signature; `ParseJWK`, `ParseJWKSet` and `NewJWKSet` are how a consumer BUILDS those arguments — before they existed the two JWK constructors took types only `internal/` could construct, so no downstream module could call them, and the facade test hid it by importing the internal package. `JWK.Kty` / `Crv` answer a `KeyType` / `Curve`, published with their registered values. A set carrying a single RSA member is refused whole, because the SDK models no RSA. `Claims` prints its shape, never its values. stdlib-only | `pkg/v1/security/token/README.md` |
| `validation/` | Value validation (ADR 0046): `Constraint`/`Violation`/`Report` aliases + `All`/`First`/`Field`/`Each`/`Check`/`Must`, the built-ins (`Required`/`AtLeast`/`AtMost`/`Between`/`Length`/`Count`/`OneOf`/`Matches`) and `Struct[T]` — the struct-tag front end whose plan is cached per type. A violation says WHERE in one path grammar (`JoinField`/`JoinIndex`, json-tag member names); every violation is collected by default; neither `Violation` nor `Report` is an `error` (`Report.Err()` converts and returns a genuine nil); a message never echoes the value. Feeds `config.Validator` without replacing it. stdlib-only | `pkg/v1/validation/README.md` |
| `data/vfs/` | Filesystem (ADR 0056): `NewOS` → a tree-confined filesystem, `NewMem` → a double that needs no temporary directory. `FS`/`WritableFS`/`AtomicWriter`/`FullFS` aliases plus the ten sentinels. Reading is `io/fs` unchanged, so `fs.WalkDir`, `fs.Glob` and `fs.ReadFile` work with no adapter. `WriteAtomic` publishes via a temporary + `rename(2)` + two flushes: on failure the previous bytes are byte-for-byte intact and no temporary survives. A refusal carries BOTH identities — `errors.Is(err, vfs.ReadFailed)` and `errors.Is(err, fs.ErrNotExist)` both answer — so the package can be adopted one call at a time. A zero mode is refused, never defaulted. stdlib-only; `NewOS` refuses at construction off its native platforms | `pkg/v1/data/vfs/README.md` |
| `security/redact/` | Display redaction (ADR 0101): `New(Config)` → a `Redactor` whose `Value` / `JSON` / `Text` / `Attrs` replace every secret it recognises — a NAME carrying one of its `Words`, a field DECLARED secret by its `Tag` (`redact:"secret"` by default, a framework's own by configuration) or its `Field` rule, a URL's credentials — within an EXACT byte bound (the writer counts every byte before writing it), scrubbing credentials before cutting a text, never mutating its input; `Document` (`JSON` + `Truncated`), `DefaultWords`, `Placeholder`/`Ellipsis`/`MinBytes`/`Unencodable`, `DocumentInvalid`/`ValueUnencodable`. A display filter, not an access control, and the package doc lists what it does NOT recognise. stdlib-only | `pkg/v1/security/redact/README.md` |
| `statemachine/` | State machines over stored entities (ADR 0120): `Define` → a `Definition` (`Initial` / `On` / `After` / `At` / `When` / `OnEnter` / `OnTransition`, every mistake refused at once by `New`), `New(ctx, def, &Config{Store: …})` → a `Machine` you `Start` / `Fire` and tell about foreign writes with `Changed` / `Deleted`, whose `Run` fires timers, deadlines and guards — one heap entry per entity, so it sleeps until the next transition due or a write and finds it in O(log N). Your `Store` (frozen at five, absence an answer) is the source of truth and `Replace` never resurrects; a `Journal` (frozen at three) keeps each `Record` across restarts. One transition per entity at a time, nothing locked across a panic, an OnEnter hook firing its own machine refused (`Reentrant`), a failing entity backed off alone. `Store`/`Journal`/`Record`/`Step`/`Trigger` alias core; `Definition`/`Machine` alias the engine's `MachineSpec`/`StateMachine` | `pkg/v1/statemachine/README.md` |
| `observe/profiling/` | The process's own profiles (ADR 0121): `CaptureCPU` (one CPU profiler per process: `ProfilerBusy`, never queued; no partial profile on cancellation) and `CaptureHeap`, `Parse` (pprof decoded with the standard library, strict and bounded), `Fold` onto owners your `Attribute` names — per-owner costs, top functions, a pruned flame graph, sums exact in the profile's own unit — `Goroutines` / `ParseGoroutines` (a dump read into goroutines: state, minutes waited, labels, stacks; never failing) and `GroupGoroutines`, `CanonicalName`. A heap profile is sampled: ask where, not how much. stdlib-only | `pkg/v1/observe/profiling/README.md` |
| `data/docstore/` | Typed, keyed JSON documents (ADR 0110): `Open(Config, indexes...)` → a `Store[T]` with `Put` / `Insert` / `Replace` / `Update` / `Delete`, `Unique` and `Index` read by `Lookup` / `Find`, hooks after every durable write; persisted through a `vfs.FullFS` as one overlay entry per write folded into one `{key: document}` snapshot, so a write's cost does not grow with the store; and `OpenSQL(SQLConfig, indexes...)` → a `SQLStore[T]`, the same contract in two tables of a PostgreSQL, MySQL or SQLite database, every call taking a context and running on the transaction it carries, with `SQLMigration` for its tables and `Reindex` for its rows (ADR 0139); `Versions` keeps the last versions of each document in the same durable write, stamped by `PutStamped` and its siblings, read by `Versions` / `Version`, held by `Held`, rewritten by `RewriteVersions` (ADR 0143) | `pkg/v1/data/docstore/README.md` |
| `data/codec/strictjson/` | One JSON document read one way (ADR 0102): `Decode(r, v, maxBytes)` on `encoding/json/v2` refuses the duplicate name, the case-only match, the unknown member, invalid UTF-8 and trailing data, reading at most the bound plus one byte; `DecodeRequest` adds `http.MaxBytesReader` (413 and a closed connection), the empty body as its own code, and 415 for a body that does not declare JSON; `PointerOf` locates a refusal as a JSON Pointer bounded to `MaxPointerBytes`. No refusal repeats the document. Not a codec Format, and not in `codec/`'s blank imports, so a server does not link sixteen codecs. stdlib-only | `pkg/v1/data/codec/strictjson/README.md` |
| `data/codec/jsonshape/` | A Go type's wire shape under encoding/json (ADR 0133): `Of` / `For[T]` → a `Shape` (`Kind`, `Format`, `Nullable`, `Fields`, `Items`, `Values`, `Ref`) whose `Field`s carry `Optional`, `Quoted`, `Rules` and the Go field behind them (`GoName`, `Tag`, `Index`). Members resolved as Go 1.27's engine resolves them — promotion through pointers and unexported structs, dominance, the tag grammar, the `embed` option — and pinned against `json.Marshal` itself. Links no codec. stdlib-only | `pkg/v1/data/codec/jsonshape/README.md` |
| `data/codec/jsonpatch/` | Two JSON documents' difference as RFC 6902 operations (ADR 0143 §D9): `Diff(from, to)` → `[]Edit` (`Op` add / remove / replace, `Path` an RFC 6901 pointer, `Value` written, `Old` replaced or removed), in the order they apply; documents read strictly and compared as RFC 6902 §4.6 does, numbers exactly; arrays aligned before they are paired; encodes as a JSON Patch document; `NotJSON` names the document and the offset, never its content | `pkg/v1/data/codec/jsonpatch/README.md` |
| `data/codec/json/`, `data/codec/yaml/`, `data/codec/toml/` | One format registered alone (ADR 0134): a blank import registers that codec and links nothing else — every codec is written on the standard library, YAML as a named subset (ADR 0156) — where `data/codec/` links every format. Each exports `Format`, an untyped constant | `pkg/v1/data/codec/{json,yaml,toml}/README.md` |
| `data/codec/bson/` | BSON alone, implemented with the standard library (ADR 0021, ADR 0134): importing it registers the codec, and it carries BSON's value types — `D`, `E`, `M`, `A`, `ObjectID`, `DateTime`, `Decimal128`, `Binary`, `Regex`, `Timestamp` and the deprecated ones — with `Marshal` / `Append` / `Unmarshal`, `ObjectIDFromHex`, `ParseDecimal128`. The driver's v1 mapping and struct tags, checked against it; malformed input refused whole before the target is written; 100 levels of nesting. stdlib-only | `pkg/v1/data/codec/bson/README.md` |
| `net/static/` | A file tree served over HTTP (ADR 0130): `New(fsys, Config)` → an `http.Handler` that cleans the name from the root, never lists a directory, falls back to the SPA shell for extension-less routes only, sends CSP / nosniff / Referrer-Policy on every response, caches content-hashed names for good, pins the web types, and answers a refused NAME 404 and a failing tree 500. stdlib-only | `pkg/v1/net/static/README.md` |
| `view/` | Server-side rendering (ADR 0058): `New(Config{FS: …})` → a `Renderer` you `Render(ctx, name, data)`, returning the complete document or nothing — it never takes an `io.Writer`, because a mid-execution failure would already have flushed the status line and half the page. The engine is the stdlib `html/template`, USED and not reimplemented: it is the only Go engine that escapes according to CONTEXT, and a hand-written escaper would be the security regression this domain exists to prevent. `text/template` has no representation at all and an AST audit fails the build on the import. **`TrustHTML` is the one bypass and the only spelling the SDK offers**; the other six html/template trust types are REFUSED in render data with the path named. **What it does NOT prevent is stated out loud: `TrustedHTML` is a type alias, so a `Trusted` the caller built wrongly is an XSS the SDK cannot see** — the domain prevents an accidental bypass and gives the deliberate one one greppable word. **Parse once**: reparsing per request costs 34× on a realistic tree. Stdlib-only, cross-OS | `pkg/v1/view/README.md` |
| `observe/logger/writer/` | Blank-import activation of the dependency-free logger writers `"console"`, `"file"` and `"rotfile"` (ADR 0012), so `logger.NewMulti` resolves those names; no exported symbols | `pkg/v1/observe/logger/writer/README.md` |
| `crypto/` | Authenticated encryption (ADR 0013): `NewKey` (exactly 32 bytes, redacting), `Seal` / `Open` with the nonce generated and hidden in the box, AES-256-GCM by default and `SealAs` for another `Algorithm` (`XChaCha20Poly1305` activates through its own blank import), `SealStream` / `OpenStream`, `WrapKey` / `UnwrapKey` passphrase envelopes (ADR 0014). The crypto family's root: the six scheme facades below are its children, and importing one never links it (ADR 0155) | `pkg/v1/crypto/README.md` |
| `crypto/agree/` | Key agreement (ADR 0014): `GenerateKey(X25519)` and `SharedKey(a, priv, peerPub, info)` → a `Key` of `KeyLen` bytes | `pkg/v1/crypto/agree/README.md` |
| `crypto/hash/` | Digests: `New` / `Sum` / `SumHex` over `SHA256`, `SHA512`, `SHA3256`, `CRC32C` and `FNV1a64`, plus the streaming `NewDigestWriter` / `NewVerifyingReader` | `pkg/v1/crypto/hash/README.md` |
| `crypto/kdf/` | Key derivation (ADR 0014): `Subkey(HKDFSHA256, …)` and `NewKeyTree`, a key hierarchy under one master `Key` | `pkg/v1/crypto/kdf/README.md` |
| `crypto/mac/` | Message authentication (ADR 0014): `Tag` / `Verify` with `HMACSHA256` over a `Key` | `pkg/v1/crypto/mac/README.md` |
| `crypto/password/` | Password hashing: `Hash` → a PHC string (`PBKDF2SHA256`), `Verify`, `NeedsRehash`; argon2id is the opt-in scheme under `third-party/x-crypto/argon2id`; `IsCommon`, one of the ten thousand most common passwords, case-insensitively (ADR 0143) | `pkg/v1/crypto/password/README.md` |
| `crypto/sign/` | Signatures: `GenerateKey` / `Sign` / `Verify` over `Ed25519` and `ECDSAP256` | `pkg/v1/crypto/sign/README.md` |
| `net/client/` | The outbound half of the network domain (ADR 0029): `New(cfg, tlsid.Identity, Policy, CallHook)` → a `Client` whose `Policy` (`AllowMethods` / `AllowPaths` / `DenyPaths` / `Policies`) is enforced in the transport; a refusal is `RequestDenied` | `pkg/v1/net/client/README.md` |
| `net/tlsid/` | TLS and mutual-TLS identities shared by `server` and `client` (ADR 0029): `New` for material in memory, `Load` for material on disk; `MaterialInvalid` | `pkg/v1/net/tlsid/README.md` |
| `net/server/` | The inbound engine (ADR 0029): `New` → a `Server` of stream `Group`s and `PacketGroup`s configured by `GroupOption`s (`Listen`, `TLS`, the timeouts, `MaxConns`, `Shards`, …), `Chain` / `ChainPacket` middlewares, and a drain announced through `DrainSignal` rather than imposed (ADR 0043) under `WithDrainTimeout` | `pkg/v1/net/server/README.md` |
| `net/sse/` | Server-Sent Events (ADR 0029, ADR 0043): `New(w, r, opts…)` → a `Stream` with keep-alive, `Retry` and a write timeout; `DrainSignal` | `pkg/v1/net/sse/README.md` |
| `net/websocket/` | WebSocket, RFC 6455 in the stdlib (ADR 0047): `Upgrade` → a `Conn`, the origin checked by default (`AllowOrigins` / `AllowAnyOrigin` widen it), frame and message ceilings, ping; `DrainSignal` | `pkg/v1/net/websocket/README.md` |
| `proc/` | Capability preflight for the process domain (ADR 0016, ADR 0018): `Supported` / `MissingCapabilities` / `MustSupport` over `Capability`, and the `UnsupportedPlatform` a primitive returns where its OS has no secure mechanism | `pkg/v1/proc/README.md` |
| `proc/process/` | Spawn a child under explicit credentials in its own process group and session (ADR 0016): `Start` / `MustStart(ctx, Spec)` → a `Process` to wait on, signal and stop with a SIGTERM→SIGKILL escalation; and the running program itself — `Build` / `ParseBuild`, `Self` (ADR 0100) | `pkg/v1/proc/process/README.md` |
| `proc/cgroup/` | Control groups (ADR 0016): `Create` / `MustCreate` → a `Group` (`WithRoot`), `Available` | `pkg/v1/proc/cgroup/README.md` |
| `proc/memlimit/` | `Apply` derives the runtime soft memory limit from the control-group allowance governing this process and installs it, returning a `Limit` that names the allowance read, the limit applied and the `Source` that decided it (ADR 0075) | `pkg/v1/proc/memlimit/README.md` |
| `proc/reaper/` | PID 1 / subreaper zombie collection (ADR 0016, ADR 0093): `New` → a `Reaper` (`WithOnReap`), `IsPID1`, `SetChildSubreaper` | `pkg/v1/proc/reaper/README.md` |
| `proc/rlimit/` | Resource limits (ADR 0016): `Apply(pid, limits)` and `PrepareSysProcAttr` over `Resource` → `Limit`, `LimitInfinity` | `pkg/v1/proc/rlimit/README.md` |
| `proc/systemd/listen/` | systemd socket activation: `Listeners` / `Files` / `WithNames`, and `Prepare` to pass listeners to a child `Spec` | `pkg/v1/proc/systemd/listen/README.md` |
| `proc/systemd/notify/` | systemd notification: `Ready` / `Reloading` / `Stopping` / `Status` / `Watchdog` / `MainPID` / `Notify`, `WatchdogInterval`, and `Listen` for the receiving side | `pkg/v1/proc/systemd/notify/README.md` |
| `proc/signal/` | `Notify` → a signal channel and its stop, `Relay` to a `Target`, `Parse` a signal name | `pkg/v1/proc/signal/README.md` |
| `proc/ipc/` | A private socket (ADR 0148): `Listen(Config)` / `Dial(ctx, Config)` over a `0700` directory, `Conn.Peer` the kernel's word on the other end where it gives one (`Verified`), `RuntimeDir(app)`; the eight codes re-exported |

The distribution packages — `git`, `selfupdate`, `entitlement` and `gate` — are
the framework's since ADR 0158: `github.com/kitsunium/sdk/framework/<name>`,
with the same surface, under the v0 licence for import paths (ADR 0155 §4).

The security facades — `authz`, `redact`, `secret`, `session` and `token` —
are children of `security/`, a family directory with no Go code of its own
(ADR 0155): `github.com/kitsunium/sdk/pkg/v1/security/<name>`, moved from
`pkg/v1/<name>` with no alias left behind, under the same v0 licence.

The network facades — `server`, `client`, `tlsid`, `sse`, `websocket` and
`static` — are children of `net/`, a family directory with no Go code of its
own (ADR 0155): `github.com/kitsunium/sdk/pkg/v1/net/<name>`. The first three
moved from the root of `pkg/v1` and the last three from under the `server`
facade, with no alias left behind, under the same v0 licence.

The process facades — `process`, `signal`, `reaper`, `rlimit`, `cgroup`,
`memlimit`, `ipc`, and the two systemd protocols `systemd/notify` and
`systemd/listen` — are children of `proc/`, the capability preflight that stays
at the family's root (ADR 0155): `github.com/kitsunium/sdk/pkg/v1/proc/<name>`.
`systemd/` itself is a directory with no Go code. They moved from the root of
`pkg/v1` with no alias left behind, under the same v0 licence, and `sdnotify`
and `sdlisten` were renamed for the path they now sit at: package `notify` and
package `listen`. A child never links its parent, so importing one does not link
the preflight.

The observability facades — `logger` (with its children `logger/writer` and
`logger/slogbridge`), `metrics`, `trace` and `profiling` — are children of
`observe/`, a family directory with no Go code of its own (ADR 0155):
`github.com/kitsunium/sdk/pkg/v1/observe/<name>`. They moved from the root of
`pkg/v1` with no alias left behind, under the same v0 licence, and the
link-time stamp moved with the logger: `x_defs` and `-ldflags -X` now name
`github.com/kitsunium/sdk/pkg/v1/observe/logger.Version` (root `CLAUDE.md`
rule 7).

The data facades — `codec` (with its per-format children `codec/json`,
`codec/yaml`, `codec/toml` and `codec/bson`, and the JSON tools that are not
Formats, `codec/strictjson`, `codec/jsonshape` and `codec/jsonpatch`), `sql`,
`docstore`, `queue`, `cache`, `vfs` and `semver` — are children of `data/`, a
family directory with no Go code of its own (ADR 0155):
`github.com/kitsunium/sdk/pkg/v1/data/<name>`. They moved from the root of
`pkg/v1` with no alias left behind, under the same v0 licence. `semver` sits
with them as the reorganisation's target tree places it, where ADR 0155 §1 and
ADR 0159 §4 wrote the root of `pkg/v1`; the kernel package it forwards to stays
at `internal/kernel/semver`. `transform` has no facade of its own: `data/codec`
blank-imports its engine and publishes the compression frame.

The `data/codec/` sub-package was added since the original CLAUDE.md. The legacy `codec/baseenc/` byte-level package was removed in favour of uniform `codec.Marshal("base64"|"base64url"|"base32"|"base16"|"hex"|"ascii85", v)` dispatch — every encoding format now goes through the same verb.

## Module

Single Go module `github.com/kitsunium/sdk/pkg` — one `go.mod` (at `pkg/go.mod`), one `go.sum`. The consumer packages live under this `v1/` directory, so import paths stay `github.com/kitsunium/sdk/pkg/v1/*`; only the module declaration sits one level up. Go forbids a `/v1` module-path suffix, so the module itself is the bare `…/pkg` (ADR 0017). Built and tested under Bazel via `//pkg/v1/...`; `cd pkg && GOWORK=off go test ./...` works for local iteration.

## Public surface contract

- **Stable identifiers.** Every exported name / type / const in `pkg/v1/*` is frozen until `pkg/v2` cuts. New helpers can be added; existing signatures cannot move.
- **Aliases, not new types.** Public types are `type X = internalPkg.X` so consumers and SDK code share the type identity (a `pkg/v1/observe/logger.Attr` passes anywhere `corelogger.AttrValue` is expected).
- **Error model is constructable; other internal types are not.** Since ADR 0019, `pkg/v1/errs` exposes `New` / `Wrap` (+ `WrapParams`) and the `Field` helpers alongside the `Of`-accessors. Consumers receive `error`, introspect it, AND mint their own typed errors in the same model — but the concrete `*errs.Error` stays unexported, so they cannot forge one by struct literal; construction routes through the runtime-validated constructors (which return a typed `CodeInvalid*` error, never panic). The kernel `errs.Define` (panic-at-init, AST-audited) stays internal; the public path is the non-panicking `New`/`Wrap`. This exception is `errs`-only — logger/codec internals keep their constructors private.
- **`FieldValue` is passed through** (as the `Field` alias) since ADR 0019, so consumers attach structured metadata at construction, and **`FieldsOf` is re-exported** so they read it back: a `Field` answers `Key()` and `StringValue()`. The re-export is verbatim, like every other accessor; the `FieldsOfAsMap(err) map[string]string` shape is still not offered: a key can appear at several depths of a chain, and a map would pick one silently.

## Version freeze policy

- `pkg/v1` signatures freeze on first `v1.0.0` tag. Breaking signature changes after the freeze go to `pkg/v2` (coexists with v1 until deprecation).
- **Pre-1.0.0 precedent for breaking changes.** Breaking changes land under `pkg/v1` while the module is v0 — ADR 0040 makes that a licence the commit must state, and it expires at v1. Among them:
  1. ADR 0002 errors-package MR — 4 breaking signature changes + 1 behaviour change (e.g. `NewText(Config{Writer: nil})` now returns `WriterRequired` instead of silently defaulting to stderr).
  2. PR #25 (`refactor!: drive ktn-linter phases 1-7 to zero issues`) — `baseenc.Encoding` migrated from untyped `string` constants to a typed `int` + `iota` block with `EncodingUnknown` as the zero-value sentinel. Caller code that compared `Encoding` to a string literal stopped compiling; the typed form makes typos catchable at the call site.
  3. PR #27 deleted the byte-level `codec/baseenc/` package itself (and `EncodingUnknown` with it) in favour of `codec.Marshal` dispatch — see Contents above.
  4. ADR 0044 re-shaped the published `metrics` types onto the OpenTelemetry data model, citing ADR 0040.
- All of them shipped before `v1.0.0`. **After v1.0.0 the policy hardens: no breaking changes in `pkg/v1`** — any further migrations go to `pkg/v2`. The pre-1.0 precedent does not authorise post-1.0 breakage.
- Security fixes in `internal/*` propagate via minor bumps on the affected module without touching `pkg/v1` — the facade re-exports, it does not duplicate.

## Conventions

- Import aliases at call sites: `logger "…/pkg/v1/observe/logger"`, `errs "…/pkg/v1/errs"`, `codec "…/pkg/v1/data/codec"`.
- `import _ "github.com/kitsunium/sdk/pkg/v1/data/codec"` is enough to activate all 16 service codecs (24 Format names incl. base-N family) — registry side-effects driven by blank imports.
- Every emitted log record carries `framework_version` via the ldflags-injected `logger.Version`. Injection recipe is in `pkg/v1/observe/logger/README.md`; under Bazel `--stamp` + `x_defs` + `tools/workspace_status.sh` (`STABLE_VERSION`) supply the same value.
- `logger.NewText(Config{Writer: nil})` returns `(nil, WriterRequired)`. `logger.NewWithSink(SinkConfig{Sink: nil})` returns `(nil, SinkConfigRequired)`. Use `logger.Default()` for the stderr one-liner.

## Rules from ADR 0138

Superseded by ADR 0154 (the charter); ADR 0138 stays as the incident's record, and its rules live here.

- **A same-package doc link resolves or it is not written**, judged on every
  platform that compiles its file; `make doclinks` (`tools/genindex
  -check-doclinks`, run by `make lint-check`) fails at the line.
- **A facade links the alias and names the member after it: `[Type].Member`.**
  `go/doc` does not collect the methods and fields of an alias, so
  `[Type.Member]` over one renders as literal brackets; and a link never
  qualifies into `internal/` (`[corequeue.Broker.Publish]`) in the one surface
  written for consumers.
- *Lesson*: 152 doc links named no symbol and rendered as bracketed text on
  pkg.go.dev and in the generated READMEs, with nothing failing.

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

- `observe/` — see `pkg/v1/observe/CLAUDE.md` (the observe family — `logger`, `metrics`, `trace`, `profiling` — and the rule that put them together; a directory with no Go code, so there is no `pkg/v1/observe` to import)
- `data/` — see `pkg/v1/data/CLAUDE.md` (the data family — `codec`, `sql`, `docstore`, `queue`, `cache`, `vfs`, `semver` — and the rule that put them together; a directory with no Go code, so there is no `pkg/v1/data` to import)
- `observe/logger/` — see `pkg/v1/observe/logger/CLAUDE.md`
- `mail/` — see `pkg/v1/mail/CLAUDE.md`
- `observe/logger/slogbridge/` — see `pkg/v1/observe/logger/slogbridge/CLAUDE.md`
- `data/codec/` — see `pkg/v1/data/codec/CLAUDE.md`
- `errs/` — see `pkg/v1/errs/CLAUDE.md`
- `events/` — see `pkg/v1/events/CLAUDE.md`
- `id/` — see `pkg/v1/id/CLAUDE.md`
- `i18n/` — see `pkg/v1/i18n/CLAUDE.md`
- `data/cache/` — see `pkg/v1/data/cache/CLAUDE.md`
- `clock/` — see `pkg/v1/clock/CLAUDE.md`
- `data/semver/` — see `pkg/v1/data/semver/CLAUDE.md`
- `cli/` — see `pkg/v1/cli/CLAUDE.md`
- `security/authz/` — see `pkg/v1/security/authz/CLAUDE.md`
- `resilience/` — see `pkg/v1/resilience/CLAUDE.md`
- `observe/metrics/` — see `pkg/v1/observe/metrics/CLAUDE.md`
- `data/queue/` — see `pkg/v1/data/queue/CLAUDE.md`
- `observe/trace/` — see `pkg/v1/observe/trace/CLAUDE.md`
- `config/` — see `pkg/v1/config/CLAUDE.md`
- `scheduler/` — see `pkg/v1/scheduler/CLAUDE.md`
- `lifecycle/` — see `pkg/v1/lifecycle/CLAUDE.md`
- `security/token/` — see `pkg/v1/security/token/CLAUDE.md`
- `validation/` — see `pkg/v1/validation/CLAUDE.md`
- `data/vfs/` — see `pkg/v1/data/vfs/CLAUDE.md`
- `view/` — see `pkg/v1/view/CLAUDE.md`
- `security/secret/` — see `pkg/v1/security/secret/CLAUDE.md`
- `security/session/` — see `pkg/v1/security/session/CLAUDE.md`
- `data/sql/` — see `pkg/v1/data/sql/CLAUDE.md`
- `data/docstore/` — see `pkg/v1/data/docstore/CLAUDE.md`
- `security/redact/` — see `pkg/v1/security/redact/CLAUDE.md`
- `statemachine/` — see `pkg/v1/statemachine/CLAUDE.md`
- `observe/profiling/` — see `pkg/v1/observe/profiling/CLAUDE.md`
- `data/codec/strictjson/` — see `pkg/v1/data/codec/strictjson/CLAUDE.md`
- `data/codec/jsonshape/` — see `pkg/v1/data/codec/jsonshape/CLAUDE.md`
- `data/codec/jsonpatch/` — see `pkg/v1/data/codec/jsonpatch/CLAUDE.md`
- `data/codec/json/`, `data/codec/yaml/`, `data/codec/toml/` — see their `CLAUDE.md`
- `data/codec/bson/` — see `pkg/v1/data/codec/bson/CLAUDE.md`
- `net/static/` — see `pkg/v1/net/static/CLAUDE.md`
- `observe/logger/writer/` — see `pkg/v1/observe/logger/writer/CLAUDE.md`
- `crypto/` — see `pkg/v1/crypto/CLAUDE.md`
- `crypto/agree/` — see `pkg/v1/crypto/agree/CLAUDE.md`
- `crypto/hash/` — see `pkg/v1/crypto/hash/CLAUDE.md`
- `crypto/kdf/` — see `pkg/v1/crypto/kdf/CLAUDE.md`
- `crypto/mac/` — see `pkg/v1/crypto/mac/CLAUDE.md`
- `crypto/password/` — see `pkg/v1/crypto/password/CLAUDE.md`
- `crypto/sign/` — see `pkg/v1/crypto/sign/CLAUDE.md`
- `security/` — see `pkg/v1/security/CLAUDE.md` (the security family — `authz`, `redact`, `secret`, `session`, `token` — and the rule that put them together; a directory with no Go code, so there is no `pkg/v1/security` to import)
- `net/` — see `pkg/v1/net/CLAUDE.md` (the net family — `server`, `client`, `tlsid`, `sse`, `websocket`, `static` — and the rule that put them together; a directory with no Go code, so there is no `pkg/v1/net` to import)
- `net/client/` — see `pkg/v1/net/client/CLAUDE.md`
- `net/tlsid/` — see `pkg/v1/net/tlsid/CLAUDE.md`
- `net/server/` — see `pkg/v1/net/server/CLAUDE.md`
- `net/sse/` — see `pkg/v1/net/sse/CLAUDE.md`
- `net/websocket/` — see `pkg/v1/net/websocket/CLAUDE.md`
- `proc/` — see `pkg/v1/proc/CLAUDE.md` (the capability preflight at the family's root, and its children — `process`, `signal`, `reaper`, `rlimit`, `cgroup`, `memlimit`, `systemd/notify`, `systemd/listen`, `ipc` — with the rule that put them together)
- `proc/process/` — see `pkg/v1/proc/process/CLAUDE.md`
- `proc/cgroup/` — see `pkg/v1/proc/cgroup/CLAUDE.md`
- `proc/memlimit/` — see `pkg/v1/proc/memlimit/CLAUDE.md`
- `proc/reaper/` — see `pkg/v1/proc/reaper/CLAUDE.md`
- `proc/rlimit/` — see `pkg/v1/proc/rlimit/CLAUDE.md`
- `proc/systemd/` — see `pkg/v1/proc/systemd/CLAUDE.md` (the two systemd protocols, `notify` and `listen`; a directory with no Go code, so there is no `pkg/v1/proc/systemd` to import)
- `proc/systemd/listen/` — see `pkg/v1/proc/systemd/listen/CLAUDE.md`
- `proc/systemd/notify/` — see `pkg/v1/proc/systemd/notify/CLAUDE.md`
- `proc/signal/` — see `pkg/v1/proc/signal/CLAUDE.md`
- `proc/ipc/` — see `pkg/v1/proc/ipc/CLAUDE.md`
- `health/` — see `pkg/v1/health/CLAUDE.md`
- `lock/` — see `pkg/v1/lock/CLAUDE.md`

## Before v1 — the shapes this layer publishes

Every `type X = internal…Y` in this tree publishes `Y`'s **shape**, not just its
name. ADR 0039 covers the interface half (extend by a sibling, never by
widening); ADR 0040 covers this half, and gives it an expiry date — a concrete
shape may change while the module is v0, said out loud, and **not after**.

`docs/pre-v1-published-shape-audit.md` is the inventory that licence
applies to: **125 concrete structs**, split by what a change to each actually
costs. 43 are construction shapes a caller fills in by field name, where adding
a field is free. The expensive group is the `*Value` shapes the SDK **returns** —
a caller destructures those, so even an added field breaks a composite literal
written without field names.

Review that list before tagging v1, not after. Regenerate it rather than trust
it: the count in it was wrong by a factor of 25 on the first pass, and looked
entirely plausible.
