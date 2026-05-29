# ADR 0012 — Writer Subsystem v2: universal-pattern alignment

## Status

Accepted

## Date

2026-05-29

## Deciders

kodflow (with 25-agent synthesis — see `.claude/contexts/logger-writers-architecture.md` §5)

## Amends

ADR 0006 §"Future allocations" — the "next logger sub-package gets PP slot 22+"
guidance is retired for the logger sub-tree; the new block reservations in §11
supersede it.

## Related

- ADR 0001 — multi-module layout
- ADR 0003 — universal codec package (registry shape model)
- ADR 0005 — dotted-quad error codes
- ADR 0010 — kernel object-recycling primitive (concrete-struct precedent)
- ADR 0011 — kernel copy-on-write snapshot primitive (storage choice for registry)
- `.claude/contexts/logger-writers-architecture.md` — research context (industry survey + 25-agent universal-pattern synthesis)

## Context

The SDK logger writer subsystem (`internal/service/logger/sink/*` + `…/middleware/*`)
shipped with a 3-method `Sink` interface (`Write` / `Flush` / `Close`),
three transports (`console`, `file`, `syslog`), six middlewares (`multi`, `async`,
`route`, `failover`, `sample`, `recover`), and ADR 0006 PP slots 13–21. The
universal-pattern audit (`.claude/contexts/logger-writers-architecture.md`)
ran a 25-agent synthesis against the codec subsystem (ADR 0003 — 10+ formats
behind one registry) and the kernel pool primitives (ADR 0010 — concrete
generic types, no interface) and concluded that the logger subsystem needs:

1. **Three new identity methods on `Sink`** (`Name` / `Class` / `Schemes`) so a
   construction-time registry can dispatch URL → factory and middlewares can
   choose defaults from class. The codec interface already carries
   `Name`/`MIMETypes`/`Extensions` for exactly this reason.
2. **A registry of factories** (not instances — sinks own resources) keyed on
   `Name` + `Schemes`, storage backed by `snapshot.Value[map[K]V]` (ADR 0011 +
   the codec 9.1 ns vs 12.8 ns measurement justifying COW over `sync.Map`).
3. **Five new reliability middlewares** (`timeout`, `retry`, `circuit_breaker`,
   `dlq`, `rate_limit`) so the upcoming remote sinks (OTLP, Loki, CloudWatch,
   S3 — each lands as its own follow-up PR) ride a battle-tested compose stack.
4. **A composition validator** (`composevalidate`) that catches the 10
   anti-patterns the compiler cannot (cycles, empty Multi, sample rate ≤ 0,
   redundant async-over-async, etc.) at construction time.
5. **A public facade `pkg/v1/logger/sink/` + `pkg/v1/logger/sinkmw/`** so the
   extension surface is `go get`-resolvable (ADR 0009).
6. **One construction-time warning primitive** so a middleware can hint
   "you're wrapping a Local sink with a CircuitBreaker — this will never trip"
   without throwing an error. The hint sink is `os.Stderr` by default and an
   installable hook in tests.
7. **A reserved PP-block allocation** so the 10+ upcoming sinks claim slots
   alphabetically without per-PR contention.

The locked architectural answers are in `.claude/contexts/logger-writers-architecture.md`
§5. This ADR is the authoritative record cited by the migration's commit
messages.

## Decision

The 15 numbered choices below are the architectural decisions the migration
carries. Each is binary — accept or reject the whole — to keep follow-up PRs
focused on implementation, not re-litigation.

### 1. `Sink` stays the name; `Encoder` stays the format port; `Handler` stays composition

Disambiguates from `io.Writer` (zerolog), `slog.Handler` (fused format+transport),
`zapcore.Core` (owns level checking). "Writer" is reserved for the underlying
`io.Writer` that a console-class sink wraps.

### 2. `Sink` interface gains `Name() string` + `Class() SinkClass` + `Schemes() []string`

```go
type Sink interface {
    Name() string             // unique registry key, kebab-case, frozen post-v1
    Class() SinkClass         // middleware compose-time hint, NEVER on hot path
    Schemes() []string        // URL schemes this sink handles, defensive-cloned on each call
    Write(ctx context.Context, r RecordEvent, p []byte) (n int, err error)
    Flush(ctx context.Context) error
    Close() error
}
```

`SinkClass uint8` is a bounded enum: `UnknownClass` (zero value) / `LocalClass` /
`AsyncLocalClass` / `RemoteBatchedClass` / `RemoteStreamingClass`. The
zero-value-as-Unknown design is the wave-2 review's safety footgun fix —
forgetting to set a class fails closed rather than silently falling back to
`LocalClass`.

### 3. Optional capability sub-interfaces detected by type assertion

```go
type BatchSink interface { Sink; WriteBatch(ctx, []RecordEvent, [][]byte) (int, error) }
type SyncSink  interface { Sink; Sync(ctx) error }   // durability (fsync), not Flush
```

Convergent with zap (`zapcore.Core` + the unexported buffered wrappers) and
phuslu/log (`Writer` + the `Sync()`-conformant variants). Decorators fall back
to N×`Write` when absent — zero breaking change for sinks that don't implement
the capability.

### 4. `*BaseSink` embed (pointer embed, 1 word) — zero-breaking migration

```go
type BaseSink struct { name string; class SinkClass; schemes []string }

func NewBaseSink(name string, class SinkClass, schemes []string) *BaseSink {
    // validates: empty name → panic; class > max → panic; duplicate scheme → panic
    return &BaseSink{name: name, class: class, schemes: slices.Clone(schemes)}
}

func (b *BaseSink) Name() string      { return b.name }
func (b *BaseSink) Class() SinkClass  { return b.class }
func (b *BaseSink) Schemes() []string { return slices.Clone(b.schemes) }
```

`*BaseSink` embed (1 word) — not value embed (5 words). Migration patch per
existing sink ≤ 5 lines. Public constructors stay byte-for-byte identical.

### 5. Sink registry = registry of factories, NOT instances

```go
type Factory interface {
    Name() string
    Schemes() []string
    FromURL(u *url.URL) (logger.Sink, error)
    FromConfig(cfg any) (logger.Sink, error)
}
```

Codec singletons are stateless; sinks own resources (endpoints, creds,
goroutines). Sticking instances in the registry would either force a singleton
or leak. The factory shape is the canonical answer (`database/sql.Register`
precedent).

### 6. Registry storage = `snapshot.Value[map[K]V]` (ADR 0011)

Two snapshots: `byName snapshot.Value[map[string]Factory]` for lookup, `byScheme
snapshot.Value[map[string]string]` for URL dispatch. Lock-free `Load` on every
`FromURL` call; mutex-serialised `Update` on every `Register` (package init only).
Same shape as the codec registry consolidated under ADR 0011.

### 7. Named-singleton registration — NO `init()`

```go
var Factory sinkreg.Factory = sinkreg.Register(&consoleFactory{})
```

Convergent with the codec convention (`internal/core/codec/registry.go` —
`var Codec = codec.Register(&fooCodec{})`). `KTN-FUNC-NOINIT` stays globally
enforced; sinks must use the package-level var idiom, not `init()`.

### 8. Multi/fanout is NOT a registered sink — it's a middleware

No URL shape can describe "fan out to N sinks"; pretending otherwise pollutes
the URL dispatch table. `multi` stays in `…/middleware/multi/`.

### 9. Driver sub-registry (`dbsink`) is the ONLY 2-level nest

`internal/service/logger/sink/db/` is a shell (batching + retry + CB + DSN
dispatch) over backend-specific drivers (`clickhouse/`, `loki-db/`,
`victorialogs/`). Mirrors `database/sql.Register`. Anti-coupling: the shell
MUST NOT import any concrete driver; drivers import the shell.

### 10. Folder layout — flat under `sink/` (revisit at 12 entries)

Alphabetical, one PP slot per entry. Beyond 12 top-level entries we revisit
`local/cloud/wire/` grouping. The post-v1 plan reaches 11 entries; ample
headroom.

### 11. PP-block reservation (amends ADR 0006)

| Range | Owner |
|---|---|
| 0.3.1.\* — 0.3.21.\* | existing logger sub-tree (no change) |
| 0.3.22.\* — 0.3.24.\* | codec extensions (existing, ADR 0006 — no change) |
| **0.3.25.\*** | encoder/null (new — PR-05) |
| **0.3.26.\*** | middleware/timeout (new — PR-07) |
| **0.3.27.\*** | middleware/retry (new — PR-07) |
| **0.3.28.\*** | middleware/circuit_breaker (new — PR-07) |
| **0.3.29.\*** | middleware/dlq (new — PR-07) |
| **0.3.30.\*** | middleware/rate_limit (new — PR-07) |
| **0.3.31.\*** | sink registry (new — PR-03) |
| **0.3.32.\*** | encoder/codec adapter (new — PR-05) |
| **0.3.33.\* — 0.3.63.\*** | sink expansion (alphabetical from 33: azure, cloudwatch, datadog, db, db/clickhouse, db/loki, db/victorialogs, gcp, loki, otlp, s3, splunk) |
| **0.3.64.\* — 0.3.79.\*** | middleware overflow (composevalidate claims 64-65) |
| **1.1.1.\*** | sinkmw facade (`pkg/v1/logger/sinkmw`) — tombstone code for invalid BackoffPolicy |

Within-package serial layout (recorded as documentary convention):
`.1` config / construction failure · `.10` ctx cancel · `.20` write failure ·
`.30` flush failure · `.40` close failure · `.50+` package-specific.

### 12. Public facade: `pkg/v1/logger/sink/` + `pkg/v1/logger/sinkmw/`

```
pkg/v1/logger/sink/      type aliases (Sink, Record, BatchSink, SyncSink, SinkClass, BaseSink, Factory)
                         forwarders (Lookup, LookupScheme, FromURL, Available)
                         blank_imports.go — tier-1 (console, file, syslog) only
                         codes.go — 0.3.31.* re-exports + 1.1.1.* facade codes
                         {console,file,syslog,mock}/
pkg/v1/logger/sinkmw/    re-exports for the 11 middlewares + shared option types
```

Tier-2 sinks (CloudWatch, S3, GCP, Azure, Datadog, Splunk) land in their own
`pkg/v1/logger/sink/<vendor>/` modules with separate `go.mod` (ADR 0001
multi-module precedent) — keeps AWS-SDK weight (~10 MB compiled) opt-in.

### 13. Class-aware middleware behavior — warnings via `sdkwarn`

Each middleware reads `downstream.Class()` at construction time (zero hot-path
cost) and emits at most one warning per call site:

| Middleware ↓ / Class → | Local | AsyncLocal | RemoteBatched | RemoteStreaming |
|---|---|---|---|---|
| async | warn (redundant) | warn (redundant) | recommended | warn (stream has its own buffer) |
| timeout | 100 ms | 100 ms | 1 s | 1 s |
| retry | n=1 (rare) | n=1 | n=3 exp+jitter | per-stream |
| circuit_breaker | warn (n/a) | warn (n/a) | on (5/30s) | on (5/30s) |
| dlq | warn (rarely useful) | warn | required for at-least-once | opt-in |
| rate_limit | OFF | OFF | OFF | opt-in |
| recover | always on | always on | always on | always on |

A `Force()` Option silences the warning per call site (avoids paternalism).
Warnings go to `internal/kernel/sdkwarn.Emit`, which writes to `os.Stderr` by
default and routes to an installable hook in tests.

### 14. Composition validation (`composevalidate`) — 10 anti-patterns, 3-tier policy

| # | Pattern | Verdict |
|---|---|---|
| 1 | Cyclical Multi (`Multi(a, Multi(a, b))`) | Error (`Validate` returns) |
| 2 | Async over Async | Warning |
| 3 | DLQ over DLQ | Warning |
| 4 | Sample rate ≤ 0 | Panic (always broken) |
| 5 | Retry over Sample | Warning (retry budget wasted) |
| 6 | CircuitBreaker over Local sink | Warning (will never trip) |
| 7 | Inner Timeout > Outer Timeout | Warning (inner ctx dead on arrival) |
| 8 | `Multi()` with 0 children | Panic (promote current silent no-op) |
| 9 | Failover primary == secondary | Error (returned from `Failover.New`) |
| 10 | Route with empty table + no fallback | Error (returned from `Route.New`) |

Each middleware adds a 5-line `Describe() string` + `Children() []Sink`
implementation; `composevalidate.Validate(root)` walks via pointer identity.
DoS protection: `MaxDepth = 1024` and `MaxDescribeBytes = 64 KiB` guards
prevent stack overflow / allocation explosion on adversarial deep chains.

### 15. URL-scheme convention

Schemes drive `FromURL` dispatch. RFC 3986 §3.1 alphabet (`ALPHA *( ALPHA / DIGIT
/ "+" / "-" / "." )`):

| Sink | Schemes |
|---|---|
| console | `stdout`, `stderr` |
| file | `file://` |
| syslog | `udp+syslog://`, `tcp+syslog://`, `tls+syslog://` |
| otlp | `otlp+http://`, `otlp+grpc://` |
| loki | `loki://` |
| s3 | `s3://` |
| cloudwatch | `cloudwatch://group/stream` |
| dbsink/clickhouse | `clickhouse://` |
| dbsink/loki-db | `loki+db://` |
| dbsink/victorialogs | `victorialogs://` |

## Consequences

- **Documentary**: ADR 0006 §"Future allocations" is amended (see PR-01 amendment paragraph). The dotted-quad table in `internal/kernel/errs/registry_external_test.go` gains the 0.3.25-79 block (PR-10) plus Layer-1 1.1.1.* coverage.
- **Source**: existing public constructors `console.New / NewStderr / NewStdout`, `file.New`, `syslog.New / NewWithConfig` keep byte-for-byte identical signatures. The `*BaseSink` embed is the migration vehicle.
- **Layering**: `core/logger` MUST NOT import `core/codec` (encoder/codec adapter lives at service layer). Enforced by Bazel visibility — `bazel query 'rdeps(//internal/core/logger/..., //internal/core/codec/...)'` stays empty.
- **Kernel**: one new primitive (`sdkwarn`) admitted by this ADR (PR-01). Stdlib-only + generic (one-shot warnings + audit hook) — passes the kernel gate.
- **Tests**: every new package ships `_internal_test.go` + `_external_test.go`; AllocsPerRun gates on the `Emit`-with-hook-installed path (PR-01) + the `sinkreg.Load` path (PR-03).
- **Hot path**: zero new allocations on `Write` / `Flush` / `Close`. All new behavior is construction-time.

## Breaking changes

None to the public API. The 3 new `Sink` methods are added in PR-04 via the
`*BaseSink` embed; existing in-tree implementations adopt the embed in the same
atomic PR without changing their public constructors. Out-of-tree
implementations (currently none — `pkg/v1/logger/sink/` does not yet exist)
will need to embed `*BaseSink` after PR-08, but the public surface is created
in that PR — no "break" exists because no consumer ships yet.

## Alternatives considered

- **A. Mega-PR (all 22+ commits, ~15000 LOC).** Rejected: bisect-hostile,
  un-reviewable, one failing CI lane stalls everything. The parent plan
  documents the rejection in §2.
- **B. Builder DSL at the core layer.** Rejected: community sinks would have
  to learn it; plain function composition (matching `samber/slog-multi`) is
  the converged industry idiom (zap, zerolog, phuslu, slog).
- **C. `sync.Map` for the registry.** Rejected: not generic, dirty-map write
  path adds machinery for write-mostly workloads the registry never triggers,
  measured 12.8 ns vs 9.1 ns against the codec snapshot — same loss the ADR
  0011 chose against.
- **D. `Reloadable` / `HealthChecker` capability sub-interfaces.** Deferred:
  `Reloadable` is achievable via atomic-swap with `snapshot.Value` outside
  the sink; `HealthChecker` waits until the circuit breaker MW lands and
  needs it concretely.
- **E. Slog-handler bridge in this PR.** Deferred to its own ADR; the slog
  interop story has design implications that deserve dedicated review.
- **F. `slog.Handler` shim for `sdkwarn`.** Rejected: `slog` is a log
  destination; `sdkwarn` is a construction-time hint channel. Wiring `sdkwarn`
  to `slog` would couple every test's audit captor to the logger under test
  — the exact dependency `sdkwarn` exists to break. `slog` callers wanting
  composition warnings in their structured stream can register a hook that
  forwards to `slog.Warn` themselves; the primitive stays minimal.

## Deferred

- New transports (otlp, loki, dbsink + drivers, cloudwatch, s3, gcp, azure,
  datadog, splunk) — each its own PR after PR-10 lands.
- `console` v2 PIPE_BUF fast path — perf PR with BENCH.md regression evidence.
- `file` v2 `O_APPEND` + `fdatasync` periodic + rotator goroutine + zstd async
  — perf PR after BENCH.md baseline is captured.
- LMAX Disruptor MPSC ring inside `async` middleware — deferred until profiling
  at >5M msg/s justifies it.
- TLS path validation for `syslog` (RFC 5425) — separate hardening PR.
- `KTN-LOGGER-SINK-REGISTERED` ktn-linter rule — separate ktn-linter PR.

## References

- ADR 0001 — multi-module layout — `docs/adr/0001-sdk-go-multimodule-layout.md`
- ADR 0003 — universal codec package — `docs/adr/0003-sdk-codec-package.md`
- ADR 0005 — dotted-quad error codes — `docs/adr/0005-sdk-error-codes-dotted-quad.md`
- ADR 0006 — error code registry extension — `docs/adr/0006-sdk-error-code-registry-extension.md` (amended by this ADR)
- ADR 0010 — kernel object-recycling primitive — `docs/adr/0010-kernel-recycler-primitive.md`
- ADR 0011 — kernel copy-on-write snapshot primitive — `docs/adr/0011-kernel-snapshot-primitive.md`
- Architecture research context — `.claude/contexts/logger-writers-architecture.md`
- Parent migration plan — `.claude/plans/logger-writer-universal-pattern.md`
