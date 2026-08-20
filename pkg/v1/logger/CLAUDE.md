<!-- updated: 2026-05-18T14:30:00Z -->
# pkg/v1/logger/

## Purpose

Stable v1 public API for SDK logging. Everything consumer code needs — `Logger`, `Attr`, `Level`, `Sink`, `Encoder`, `Builder`, `Config`, `NewText`, `Default`, `NewWithSink`, `Build`, `LogAttrs`, the `Info|Warn|Error|Debug` emission helpers, and the `String|Int|Bool|Float64|Int64|Uint64|Duration|Time|Any` `Attr` constructors — is re-exported here as type aliases + thin wrappers onto `internal/core/logger`, `internal/core/logger/level`, and `internal/service/logger`. Signatures freeze post-v1.0.0.

## Contents

```
logger.go      — Logger / Attr / Level aliases, 4 Level constants, Config struct,
                 NewText, Default, Debug|Info|Warn|Error emission helpers,
                 String|Int|Bool|Float64|Int64|Uint64|Duration|Time|Any Attr constructors
sink.go        — Sink / Record / Encoder aliases, SinkConfig struct, NewWithSink,
                 Multi fan-out helper, ConsoleStderr|ConsoleStdout, TextEncoder
writer.go      — WriterName / *Config aliases, WriterSpec, NewMulti (named writers)
fromconfig.go  — Format alias, FromConfig (build a Logger from a config blob),
                 + parseLevel / decode / resolve helpers (ADR 0014 §D5)
topology.go    — TopologyConfig DTO (Level + Writers)
writer_entry.go — WriterEntryConfig DTO (Name + raw Options map)
builder.go     — Builder alias, Build (chainable hot path), LogAttrs (slice overload)
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

## Conventions

- **Aliases over wrappers.** `Logger` / `Attr` / `Level` / `Sink` / `Encoder` / `Record` / `Builder` are type aliases — same Go type identity as the internal value. Functions (`NewText`, `Default`, `Build`, …) are thin: nil-validate, delegate, decorate with `framework_version`.
- **Explicit construction.** `NewText(Config{Writer: nil})` returns `(nil, WriterRequired)` — `1.1.0.1`. `NewWithSink(SinkConfig{Sink: nil})` returns `(nil, SinkConfigRequired)` — `1.1.0.2`. The pre-errors API silently defaulted to `os.Stderr`; that was a breaking change (ADR 0002). Callers wanting the stderr one-liner use `Default()`.
- **`framework_version` on every record.** Both `NewText` and `NewWithSink` finalise their Logger via `base.With(AttrValue{Key: "framework_version", Value: StringValue(FrameworkVersion())})` so every emitted record carries the version. `FrameworkVersion()` returns the link-time `Version` var, or the `"dev"` sentinel when unset.
- **Version stamping.** `Version` is the single injection point for the SDK.
  - Raw go build: `go build -ldflags "-X github.com/kitsunium/sdk/pkg/v1/logger.Version=v0.1.0" ./...`
  - Under Bazel: `--stamp` + `x_defs` + `tools/workspace_status.sh` (which prints `STABLE_VERSION`). Both pipelines write into the same `Version` symbol.
- **Hot path.** `Build(lg, lv).Str(...).Int(...).Send(ctx, msg)` runs through a `sync.Pool`-backed builder owned by `svclogger`; steady-state per-call cost is **one** heap allocation per emit once the pool is warm — the pool recycles the builder and its attrs scratchpad, but the handler clones that scratchpad on every `Send`, so one slice escapes. `Build` trades the variadic-slice allocation for the handler's clone; prefer it for ergonomics, NOT as an allocation-free guarantee. `LogAttrs` is the slice-overload that avoids the variadic-slice allocation in `Logger.Log(... Attr)`. Numbers in `BENCH.md`; the contract is pinned by `TestV116BuildSendAllocatesOnePerEmit` in `builder_integration_test.go` (`//go:build !race`, so it runs only in the race-off alloc lane — root `CLAUDE.md` rule 12). Callers MUST NOT use a `Builder` after `Send` — it returns to the recycler.
- **Error codes use range 1.1.0.*** per ADR 0005:
  - `1.1.0.1` `CodeWriterRequired` / `WriterRequired`
  - `1.1.0.2` `CodeSinkConfigRequired` / `SinkConfigRequired`
  - `1.1.0.3` `CodeWriterSpecInvalid` / `WriterSpecInvalid`
  - `1.1.0.4` `CodeTopologyInvalid` / `TopologyInvalid` (FromConfig; redacted)

## Do NOT

- Import `github.com/kitsunium/sdk/internal/*` from consumer code. Go's `internal/` rule blocks it AND API-wise stay on `pkg/v1/*` for long-term stability.
- Set `Version` at runtime from application code. Use the ldflags recipe (or Bazel `--stamp`) so every binary commits its version at link time.
- Use a `Builder` after `Send` — the next caller will reuse the same struct from the `sync.Pool`.
- Re-export internal sink / middleware constructors here ad hoc. The current convenience helpers (`Multi`, `ConsoleStderr`, `ConsoleStdout`, `TextEncoder`) are deliberate; richer outputs reach into `internal/service/logger/{sink,middleware}` until contracts stabilise enough for a re-export.
- Forge SDK errors from consumer code via `errs.Define` — introspect via `pkg/v1/errs` accessors instead.

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
