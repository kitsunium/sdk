# ADR 0012 — Logger writer registry (named, config-driven Sink factories)

## Status

Accepted

## Date

2026-05-29

## Deciders

kodflow

## Amends

ADR 0005 §Registry, ADR 0006 §Registry (adds the `writer` code-range
allocations below; the predecessor tables stay immutable)

## Context

The logger is already multi-sink. `internal/core/logger.Sink` is the
transport port (`Write(ctx, RecordEvent, []byte)`, `Flush`, `Close`), three
terminal sinks exist (`console`, `file`, `syslog`), and `middleware/multi`
already fans one record out to N branches with `errors.Join` aggregation.
What the public surface lacks is a **named, config-driven, per-destination**
way to wire those sinks. `pkg/v1/logger` only exposes them as bare
`io.Writer` (`Config.Writer` / `Config.Writers`) or as a hand-built `Multi`
of `Sink` values; there is no `"console"` / `"file"` / `"s3"` /
`"cloudwatch"` key a consumer can name with its own configuration
(`path` / `level` / credentials).

The codec domain already solved the "resolve a name to an implementation"
problem: `internal/core/codec` holds a process-wide registry
(`snapshot.Value`-backed, ADR 0011), concrete codecs self-register at package
load via `var Codec = codec.Register(...)`, and `pkg/v1/codec` blank-imports
all of them so `import _ ".../pkg/v1/codec"` activates the full set. Dispatch
is uniform: `codec.Marshal(format, v)`.

We want the same shape for log transports, plus two new destinations — **S3**
and **CloudWatch** — that drag the AWS SDK. The SDK is deliberately dep-light
(kernel/core are stdlib-only; only `internal/service/codec/*` carry vetted
third-party encoders). The AWS dependency must never enter the module graph of
a consumer who does not use those writers.

## Decision

Introduce a **`writer`** concept: a *named, config-driven factory that
produces a logger `Sink`*. A writer does **not** replace `Sink`, and `Sink` is
**not** renamed — a writer *constructs* a `Sink` at logger-build time; the hot
path (`Sink.Write`) is untouched. This is the codec registry pattern applied
to transports.

### Core port — `internal/core/writer` (layer 2, stdlib + kernel only)

```go
type Name string                 // canonical key: "console", "file", "s3", …
type Config any                  // opaque per-writer payload, type-asserted by each factory

type Factory interface {
    Name() Name
    Open(cfg Config) (corelogger.Sink, error)
}
```

A process-wide registry mirrors `core/codec/registry.go` exactly — a
`snapshot.Value[map[Name]Factory]` (lock-free `Load`, mutex-serialised
copy-on-write writers, idempotent re-registration, loud dotted-quad panic on a
conflicting duplicate). Registration is **package-level `var`, not `init()`**
(the SDK idiom; `KTN-FUNC-NOINIT` forbids `init()`):

```go
func Register(f Factory) Factory          // var Writer = writer.Register(&fileFactory{})
func Lookup(n Name) (Factory, bool)
func Open(n Name, cfg Config) (corelogger.Sink, error)   // Lookup + Open; CodeWriterUnknownName on miss
func Available() []Name
```

This admits a **third sibling beside `codec/` and `logger/`** in
`internal/core`. The layer's purpose statement (`internal/core/CLAUDE.md`) is
widened accordingly: core now declares the contracts for *codecs, the logger,
and log-transport writers*.

### Concrete factories — `internal/service/writer/{console,file}`

AWS-free, in the existing `internal/service` module. Each is a thin factory
that **delegates to the existing terminal sink**:

```go
var Writer = writer.Register(&fileFactory{})
func (*fileFactory) Open(cfg writer.Config) (corelogger.Sink, error) {
    c, ok := cfg.(logger.FileConfig)        // type-assert the public config
    if !ok { return nil, errs.Define-backed CodeWriterConfigInvalid }
    return filesink.New(c.Path)             // reuse the existing hardened sink
}
```

Per-writer `MinLevel` (optional, zero = inherit the handler-global level) is
realised by wrapping the produced sink with the existing `middleware/route`
level predicate — no new filtering primitive.

### AWS factories — `third-party/aws/writer/{s3,cloudwatch}` (root umbrella module)

The AWS SDK must stay out of the module graph of `pkg/v1` consumers, but it does
**not** need a *new* module to do so. The repo's **root umbrella module**
(`github.com/kitsunium/sdk`) is empty today and — critically — **no other module
requires it** (`pkg/v1` requires only `internal/*`). So the AWS writers live as
packages of the root module under `third-party/*`, and the AWS `require`s go in
the **existing root `go.mod`**. Because nothing requires the root module, a
`go get github.com/kitsunium/sdk/pkg/v1` pulls **zero** AWS modules; a consumer
who wants S3 imports `github.com/kitsunium/sdk/third-party/aws/writer/s3`, which
resolves to the root module and pulls the AWS SDK.

This is the key property of Go modules: a `require` is transitive at the
*module* level regardless of what is compiled, so isolation is achieved by
hosting the dep in a module the consumer does not require — here the root
umbrella, so **no new `go.mod`** is added. The `third-party/*` namespace marks
"depends on an external vendor".

Consumers opt in with a blank import:

```go
import _ "github.com/kitsunium/sdk/third-party/aws/writer/s3"   // pulls the AWS SDK, registers "s3"
```

Without that import the AWS dependency is never compiled or linked, and the
`"s3"` name simply stays unregistered — `writer.Open("s3", …)` returns
`CodeWriterUnknownName` at **runtime**, exactly as `codec.Marshal("json", …)`
misses when its codec package was not imported. The import gates the
*dependency*; name resolution is *runtime* — consistent with the codec
precedent.

`internal/` visibility holds: `third-party/*` shares the
`github.com/kitsunium/sdk/` import-path root, so it may import
`internal/core/writer` (Go's `internal/` rule is prefix-based, not
module-based). Bazel package-group visibility is widened (`//third-party/...`)
to grant these packages access to `//internal/core/writer` and the registry.

S3/CloudWatch write over the network, so each factory wraps the produced sink
with the existing `middleware/async` (non-blocking ring + drainer) for batched
delivery driven by `FlushEvery` / `MaxBatchBytes`, surfacing losses under
back-pressure through an observable `OnDrop` hook. The hot path never blocks on
network latency.

### Config value types — `internal/core/writer` (layer 2, AWS-free)

The per-writer config structs are **plain data** — bucket/region/prefix
strings, durations, and a `CredentialProvider` interface — so they carry **zero
AWS types**. They live in `core/writer` as value types, NOT in `pkg/v1/logger`:
a `service/` or AWS-module factory must type-assert the concrete config, and a
factory importing `pkg/*` would breach the layer firewall. Placing them in core
follows the SDK's established "value type in core, alias in `pkg/v1`" pattern
(`Attr = AttrValue`):

```go
// internal/core/writer
type ConsoleConfig struct { Stream ConsoleStream; MinLevel level.Level }
type FileConfig    struct { Path string; MinLevel level.Level }
type S3Config      struct { Bucket, Region, Prefix, Endpoint string; Credentials CredentialProvider
                            FlushEvery time.Duration; MaxBatchBytes int; MinLevel level.Level }
type CloudWatchConfig struct { Group, Stream, Region, Endpoint string; Credentials CredentialProvider
                               FlushEvery time.Duration; MinLevel level.Level }
```

### Public facade — `pkg/v1/logger` (stays AWS-free)

`pkg/v1/logger` re-exports the core types as aliases and adds the ergonomic
entry point:

```go
type WriterName = writer.Name
type ConsoleConfig = writer.ConsoleConfig   // … FileConfig / S3Config / CloudWatchConfig likewise
type WriterSpec struct { Name WriterName; Config any }
func NewMulti(min Level, specs ...WriterSpec) (Logger, error)   // folds resolved sinks through Multi
```

`logger.S3Config{}` is therefore usable as a value (it is the same type as
`writer.S3Config`); it only *resolves* to a working sink once the consumer
imports the AWS writer package — honouring the "`logger.S3Config` just works
after the import" goal while keeping `pkg/v1/logger`'s own dependency graph free
of AWS.

A `pkg/v1/logger/writer` blank-import sub-package activates console + file
(the dep-free pair); `third-party/aws/writer/{s3,cloudwatch}` are imported individually
by consumers who want them.

### Credentials & secrets (rule 4 — Public/Private split)

Credentials are **never a string field**. A `CredentialProvider` yields
short-lived credentials on demand; the `Credentials` value type redacts its
secret (`String()` / `Format()` return `"<redacted>"`) so an accidental `%v`
or log emission never leaks the token. Factories pass the provider to the AWS
client and MUST NOT place token material into any `errs` `Public` (on the wire)
or `Private` (log-only) field.

## Error-code allocation (ADR 0005 / 0006 registry — amended by this ADR)

| Package | Block | Codes |
|---|---|---|
| `internal/core/writer` | `0.2.3.*` | `CodeDuplicateRegistration=0.2.3.1`, `CodeWriterUnknownName=0.2.3.2`, `CodeWriterConfigInvalid=0.2.3.3`, `CodeWriterNil=0.2.3.4`, `CodeWriterNameEmpty=0.2.3.5` |
| `internal/service/writer/console` | `0.3.22.*` | reserved — delegates to `sink/console`; returns the shared `core/writer.WriterConfigInvalid` on a wrong-type config |
| `internal/service/writer/file` | `0.3.23.*` | reserved — delegates to `sink/file`; returns the shared `core/writer.WriterConfigInvalid` |
| `third-party/aws/writer/s3` | `0.3.35.*` | `CodeS3ClientInitFailed=0.3.35.2`, `CodeS3PutFailed=0.3.35.20` (config errors reuse the shared `WriterConfigInvalid`). **Re-allocated off `0.3.24.*` (V92/V99 collision with `baseenc`); the authoritative row now lives in ADR 0015's table.** `FlushFailed`/`CloseFailed` were never minted — Flush/Close propagate the existing `CodeS3PutFailed`. |
| `third-party/aws/writer/cloudwatch` | `0.3.25.*` | `CodePutFailed=0.3.25.20`, `CodeFlushFailed=0.3.25.30`, `CodeCloseFailed=0.3.25.40` |
| `pkg/v1/logger` (writer facade) | `1.1.0.*` | `CodeWriterSpecInvalid=1.1.0.3` |

The single `WriterConfigInvalid` sentinel (`0.2.3.3`, in `core/writer`) is
returned by every factory on a wrong-type `Config`, tagged with an `errs`
`Field` naming the offending writer — so the dep-free console/file factories
need no `codes.go` of their own.

Slot convention follows the sinks: `.1–.9` construction/validation, `.20`
write/put, `.30` flush, `.40` close. The errs AST audit
(`internal/kernel/errs/registry_external_test.go`, run under Bazel) walks
`internal/` + `pkg/` and enforces code-identifier uniqueness,
`reason == screamingSnake(varName)`, and string-literal `Public` — including
the `third-party/*` packages in the root module.

## Consequences

**Positive**

- Uniform, codec-style transport selection: `WriterSpec{Name, Config}`.
  Format-/destination-swap is a one-line change at the call site.
- Reuses every existing primitive — `multi` (fan-out), `route` (per-writer
  level), `async` (network batching), `errs` (typed errors), `snapshot`
  (registry), and the three terminal sinks. Nothing new on the hot path.
- AWS dependency strictly isolated in the **root umbrella module** (which no
  other module requires); non-AWS consumers pay nothing — no `go.sum` entry, no
  link cost, **and no new `go.mod`**.
- `Sink` and the entire `sink/*` + `middleware/*` tree are unchanged —
  backwards-compatible; `NewText` / `NewWithSink` stay frozen (rule 7).

**Negative / trade-offs**

- `pkg/v1/logger` exposes `S3Config` / `CloudWatchConfig` that only resolve
  once the matching AWS writer package is imported. Misuse surfaces as a
  runtime `CodeWriterUnknownName`, not a compile error — accepted, and
  identical to the codec precedent. Mitigation: the error message names the
  missing import.
- The root umbrella module — a pure `go.work`/Bazel anchor until now — gains
  production code and the AWS `require`s. Accepted: it reuses an existing module
  (no new `go.mod`) and remains un-required by any other module, so the
  isolation holds. Releasing the AWS writers tags the root module rather than a
  dedicated one (revisit if independent versioning is needed).
- The core layer grows a third sibling. Accepted by widening the layer's
  documented purpose (see `internal/core/CLAUDE.md`).

## Testing

The func-type seam (`uploadFunc` / `deliverFunc`) makes the writer logic
testable in three layers:

1. **Unit** — batching, flush, ticker, drop, level-gate and registry are
   exercised offline by injecting fakes/recording closures. Runs everywhere.
2. **Contract (hermetic, in CI)** — the **real** `client.PutObject` /
   `PutLogEvents` path is exercised with the AWS SDK's own in-process test seam:
   a smithy `Initialize` stub middleware (`Options.APIOptions`) captures the
   typed input and short-circuits before any network. This covers the SDK-call
   closure that fakes cannot, with no Docker and no network — so it ships in
   `bazel test //...`.
3. **Integration (LocalStack, opt-in)** — `//go:build localstack` tests point
   `S3Config.Endpoint` / `CloudWatchConfig.Endpoint` at a LocalStack container,
   perform a real upload/delivery, and read it back. Excluded from the default
   build; run in a dedicated CI job with a LocalStack service.

The optional `Endpoint` field (path-style for S3) enables layer 3 and doubles
as a real feature for S3-compatible backends (MinIO, GovCloud).

## Alternatives considered

- **Rename `Sink` → `Writer`.** Rejected: churns core + six middlewares +
  three sinks + the public facade for zero behavioural gain, collides with
  `io.Writer`, and breaks the frozen `Sink` contract every handler depends on.
- **A dedicated NEW module for the AWS writers.** Rejected: it would isolate the
  dep equally well but adds a 6th `go.mod` for no benefit over reusing the
  already-isolated, no-one-requires-it root umbrella module.
- **AWS writers in `pkg/v1`.** Rejected: the AWS dep would land in
  `pkg/v1/go.mod` and pollute the module graph of **every** `pkg/v1` consumer
  (logger/codec/errs users) even when never compiled — the dep-light regression
  this ADR exists to avoid.
- **Build tags for S3/CloudWatch.** Rejected: a tag still records the dep in
  `go.mod` (`go mod tidy` resolves all build configs) and complicates Bazel.

## References

- ADR 0003 — universal codec package (registry pattern this mirrors)
- ADR 0005 — dotted-quad error codes (registry table amended above)
- ADR 0006 — error-code registry extension (sink/middleware slots)
- ADR 0011 — kernel copy-on-write snapshot primitive (registry mechanism)
- `internal/core/logger/sink.go` — the `Sink` port a writer produces
- `internal/service/logger/middleware/{multi,route,async}` — reused primitives
