<!-- updated: 2026-05-18T14:30:00Z -->
# pkg/v1/logger/

## Purpose

Stable v1 public API for SDK logging. Everything consumer code needs — `Logger`, `Attr`, `Level`, `Sink`, `Encoder`, `Builder`, `Config`, `NewText`, `Default`, `NewWithSink`, `Build`, `LogAttrs`, the `Info|Warn|Error|Debug` emission helpers, the `String|Int|Bool|Float64|Int64|Uint64|Duration|Time|Any` `Attr` constructors, and the `Kind` discriminant with its nine constants — is re-exported here as type aliases + thin wrappers onto `internal/core/logger`, `internal/core/logger/level`, and `internal/service/logger`. Signatures freeze post-v1.0.0.

## Contents

```
logger.go      — Logger / Attr / Level aliases, 4 Level constants, Config struct,
                 NewText, Default, Debug|Info|Warn|Error emission helpers,
                 String|Int|Bool|Float64|Int64|Uint64|Duration|Time|Any Attr constructors
sink.go        — Sink / Record / Encoder aliases, SinkConfig struct, NewWithSink,
                 Multi fan-out helper, ConsoleStderr|ConsoleStdout, TextEncoder
                 (ConsoleConfig{} / StreamStderr is the zero value — ADR 0030)
writer.go      — WriterName / *Config aliases, WriterSpec, NewMulti (named writers)
fromconfig.go  — Format alias, FromConfig (build a Logger from a config blob),
                 + parseLevel / decode / resolve helpers (ADR 0014 §D5)
topology.go    — TopologyConfig DTO (Level + Writers)
writer_entry.go — WriterEntryConfig DTO (Name + raw Options map)
builder.go     — Builder alias, Build (chainable hot path), LogAttrs (slice overload)
tracecontext.go — TraceContext / TraceContextSource aliases + TraceContextFromContext,
                 the ONLY bridge between the logging and tracing domains (ADR 0062)
kind.go        — Kind alias + KindAny|Bool|Duration|Float64|Int64|String|Time|Uint64|Group
                 constants (what Value.Kind() returns; needed to assert on MemorySink records)
version.go     — Version var (ldflags injection point), FrameworkVersion()
codes.go       — CodeWriterRequired / CodeSinkConfigRequired / CodeWriterSpecInvalid
                 / CodeTopologyInvalid (range 1.1.0.*)
errors.go      — WriterRequired / SinkConfigRequired / WriterSpecInvalid /
                 TopologyInvalid sentinels (errs.Define)
```

`README.md` is the consumer-facing quickstart (text-on-stderr example, custom sink topology, Builder hot path).

## `FromConfig` — build a Logger from a config blob (ADR 0014 §D5)

`FromConfig(format codec.Format, raw []byte) (*Logger, error)` is the capstone of
the config-driven writer subsystem: it builds a fully wired Logger from a config
file with **zero Go glue**. It unmarshals `raw` into a `TopologyConfig{Level,
Writers}` where each `WriterEntryConfig{Name, Options map[string]any}` names a
registered writer and carries its raw option map (decoded from the blob's
`config` key), resolves each entry against the writer registry, composes the
sinks via `Multi`, and returns a Logger filtered at the topology's `Level`.
(`Format` is a `= internal/core/codec.Format` alias; the structs carry the
`Config` role suffix per `KTN-STRUCT-ROLE`.)

- **Per-writer config translation.** Each resolved Factory is type-asserted to
  `core/writer.Decoder`. When it implements one, `Decode(entry.Options)` owns the
  translation; otherwise the raw `map[string]any` is handed straight to
  `Factory.Open` (the default mapping).
- **Dep-light invariant (LOCKED).** `FromConfig` imports ONLY the **core/codec
  dispatch surface** (`corecodec.Lookup` + `Codec.Unmarshal`) and decodes with a
  codec the **consumer already registered** (blank-import `pkg/v1/codec` or a
  single service codec). It MUST NOT blank-import `pkg/v1/codec` or any service
  codec from this package — otherwise every `pkg/v1/logger` consumer inherits the
  four vendor codec modules. Proof:

  ```sh
  cd pkg/v1 && GOWORK=off go list -deps ./logger/... \
    | grep -iE 'fxamacker|yaml|pelletier|vmihailenco|x/crypto' && echo LEAK || echo "dep-light OK"
  ```

- **Secret gate (LOCKED).** S3 / CloudWatch `Decode` parse credentials from the
  option map. `TopologyInvalid` and every error path REDACT: they name only the
  writer Name and the failure kind — never a decoded credential or option value.
  The `map[string]any` contents are never echoed into `Public` / `Private` /
  `Fields`.
- **Errors.** `TopologyInvalid` (1.1.0.4 / `TOPOLOGY_INVALID`) on a malformed
  blob, an unregistered format, an empty writer list, an unknown writer Name, or a
  Factory / `Decode` rejection — all redacted.

## Trace correlation — `trace_id` / `span_id` (ADR 0062)

Every Logger built here is correlated: `NewText`, `NewWithSink` (and therefore
`Default`, `DefaultMulti`, `NewMulti`, `FromConfig`) bind
`TraceContextFromContext` into the service-layer Logger, so a record emitted
inside a span carries that span's identity. **No caller changes a line of code**
— `Logger.Log` has always taken a `context.Context`, so nothing was widened and
ADR 0039 never came into play.

- **Top-level fields, not attributes.** `trace_id` (32 lowercase hex) and
  `span_id` (16), rendered between the header and the attributes in text and as
  top-level keys in JSON — the shape OpenTelemetry prescribes for non-OTLP log
  formats (`specification/compatibility/logging_trace_context.md`). They are a
  `Record` FIELD (`Record.TraceContext`), which is why `WithGroup("http")`
  cannot turn them into `http.trace_id`. `trace_flags` is deliberately not
  emitted.
- **The two keys are RESERVED at the top level** (ADR 0070). An attribute named
  `trace_id` or `span_id` renders as `attr.trace_id` / `attr.span_id` rather
  than beside the SDK's field: two members of one JSON object with that name
  let a decoder keep the caller's value as the line's correlation. It is
  RENAMED and never dropped, and a grouped key (`http.trace_id`) is untouched.
  The whole `attr.` namespace goes with them — a key already inside it is
  prefixed again — because a rename that is not injective just moves the
  collision one name over.
- **No span ⇒ nothing emitted.** Not `trace_id=""`, not 32 zeroes. An all-zero
  identifier is invalid under W3C Trace Context §3.2.2.3/§3.2.2.4 and would put
  an unjoinable field on every line logged outside a request.
- **No extra allocation.** 1 alloc/op with and without a span, and the same
  byte counts — the ids are `hex.Encode`d straight into the handler's borrowed
  buffer and never become a Go string. Pinned by
  `TestT34TraceCorrelationAddsNoAllocation` (exactly 1, all three emission
  paths, both cases) in the race-off alloc lane; measured and profiled in
  `BENCH.md`.
- **Where the bridge lives, and why here.** `internal/service/logger` and
  `internal/service/trace` are siblings and may not import each other, and
  `internal/core/logger` stays stdlib-only so a consumer who only wants stderr
  does not compile the trace model. The port (`core/logger.TraceContextSource`)
  is declared in core, and this package — the top layer, already allowed to know
  both domains — is the single place that binds it to
  `internal/core/trace.SpanContextFromContext`. The edge stops at `core/trace`:
  the OTLP exporters and `net/http` in `internal/service/trace` are not pulled in.
- **Populating the context** is the `trace` domain's job:
  `trace.ContextWithSpanContext`, or the `ServerMiddleware` that reads the
  inbound `traceparent` (ADR 0051).

## Conventions

- **Handing a Logger to an slog-typed API.** A library whose logging knob is the
  concrete `*slog.Logger` (e.g. `mcp.ServerOptions.Logger`) is served by
  `pkg/v1/logger/slogbridge`: `slogbridge.New(lg)` returns a `*slog.Logger`
  writing through `lg`. Do NOT build a second `slog.Handler` beside the SDK
  Logger — that pattern yields two thresholds, two line formats on one stream,
  and `framework_version` on half the records (ADR 0032).
- **Aliases over wrappers.** `Logger` / `Attr` / `Level` / `Sink` / `Encoder` / `Record` / `Builder` are type aliases — same Go type identity as the internal value. Functions (`NewText`, `Default`, `Build`, …) are thin: nil-validate, delegate, decorate with `framework_version`.
- **Explicit construction.** `NewText(Config{Writer: nil})` returns `(nil, WriterRequired)` — `1.1.0.1`. `NewWithSink(SinkConfig{Sink: nil})` returns `(nil, SinkConfigRequired)` — `1.1.0.2`. The pre-errors API silently defaulted to `os.Stderr`; that was a breaking change (ADR 0002). Callers wanting the stderr one-liner use `Default()`.
- **`framework_version` on every record.** Both `NewText` and `NewWithSink` finalise their Logger via `base.With(AttrValue{Key: "framework_version", Value: StringValue(FrameworkVersion())})` so every emitted record carries the version. `FrameworkVersion()` returns the link-time `Version` var, or the `"dev"` sentinel when unset.
- **Version stamping.** `Version` is the single injection point for the SDK.
  - Raw go build: `go build -ldflags "-X github.com/kitsunium/sdk/pkg/v1/logger.Version=v0.1.0" ./...`
  - Under Bazel: `--stamp` + `x_defs` + `tools/workspace_status.sh` (which prints `STABLE_VERSION`). Both pipelines write into the same `Version` symbol.
- **Hot path.** `Build(lg, lv).Str(...).Int(...).Send(ctx, msg)` runs through a `sync.Pool`-backed builder owned by `svclogger`; steady-state per-call cost is **one** heap allocation per emit once the pool is warm — the pool recycles the builder and its attrs scratchpad, but the handler clones that scratchpad on every `Send`, so one slice escapes. `Build` trades the variadic-slice allocation for the handler's clone; prefer it for ergonomics, NOT as an allocation-free guarantee. `LogAttrs` is the slice-overload that avoids the variadic-slice allocation in `Logger.Log(... Attr)`. Numbers in `BENCH.md`; the contract is pinned by `TestV116BuildSendAllocatesOnePerEmit` in `builder_integration_test.go` (`//go:build !race`, so it runs only in the race-off alloc lane — root `CLAUDE.md` rule 12). That guard measures **depth** — one sink — and says nothing about **width**, which is how a fan-out that heap-allocated once per record went unnoticed: `Multi` opened a per-`Write` error slate sized `len(branches)`, and a capacity that is not a constant escapes the compiler's implicit stack budget at three branches, so a third destination silently cost an extra allocation on every healthy emit. `TestFanoutWidthAddsNoAllocation` in `fanout_integration_test.go` now pins the other half, differentially against the width-1 baseline rather than against a hardcoded count. Callers MUST NOT use a `Builder` after `Send` — it returns to the recycler.
- **Error codes use range 1.1.0.*** per ADR 0005:
  - `1.1.0.1` `CodeWriterRequired` / `WriterRequired`
  - `1.1.0.2` `CodeSinkConfigRequired` / `SinkConfigRequired`
  - `1.1.0.3` `CodeWriterSpecInvalid` / `WriterSpecInvalid`
  - `1.1.0.4` `CodeTopologyInvalid` / `TopologyInvalid` (FromConfig; redacted)
  - The `slogbridge/` subpackage owns the adjacent block `1.1.1.*` — `1.1.1.1`
    `CodeLoggerRequired` / `LoggerRequired` (ADR 0032).

## Do NOT

- Import `github.com/kitsunium/sdk/internal/*` from consumer code. Go's `internal/` rule blocks it AND API-wise stay on `pkg/v1/*` for long-term stability.
- Set `Version` at runtime from application code. Use the ldflags recipe (or Bazel `--stamp`) so every binary commits its version at link time.
- Use a `Builder` after `Send` — the next caller will reuse the same struct from the `sync.Pool`.
- Re-export internal sink / middleware constructors here ad hoc. The current convenience helpers (`Multi`, `ConsoleStderr`, `ConsoleStdout`, `TextEncoder`) are deliberate; richer outputs reach into `internal/service/logger/{sink,middleware}` until contracts stabilise enough for a re-export.
- Build a parallel `slog.Logger` pointed at the same stream as an SDK Logger.
  Use `slogbridge` so there is one pipeline, one threshold, one format (ADR 0032).
- Forge SDK errors from consumer code via `errs.Define` — introspect via `pkg/v1/errs` accessors instead.
- Add `trace_id` / `span_id` yourself with `logger.String(...)`. They are stamped
  automatically from the context, as top-level record fields; a hand-added
  attribute costs a second allocation and renders under `attr.` at the top level
  (ADR 0070) or group-prefixed under `WithGroup` — in neither case as the
  correlation (ADR 0062). To carry somebody else's identifier, name it for what
  it is: `logger.String("upstream_trace_id", id)`.
- Import `internal/core/trace` from anywhere else in the logging tree.
  `tracecontext.go` is the only place the two domains meet, on purpose.

## Verification

```
bazel test --config=race //pkg/v1/logger:logger_test
# Fallback:
cd pkg/v1 && GOWORK=off go test -race -cover ./logger/...
# expected coverage: >= 90%
```

`logger_external_test.go` covers the happy path + `WriterRequired`; `sink_external_test.go` covers `NewWithSink` / `SinkConfigRequired` / `Multi` / `Build` / `LogAttrs` / `WithGroup`; `builder_external_test.go` exercises the chainable hot path; `version_external_test.go` pins `FrameworkVersion()` non-empty contract.

## Accepted audit findings

- Deferred/accepted low+info audit findings (V102, V107) are recorded in `.claude/contexts/sdk-audit-2026-06-03-accepted.yaml` (2026-06-03 close-out). Each is a deliberate decision or deferred change, not an open bug.
