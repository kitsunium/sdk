# ADR 0002 — SDK `errs` Package (Layered Typed Errors)

**Status**: Accepted
**Date**: 2026-04-19
**Deciders**: @kodflow
**Supersedes**: none
**Related**: ADR 0001 (multi-module layout), plan `sdk-errs-package-migration`

## Context

Until this MR the SDK signalled constructor failures by returning `nil`
(for example `service/logger.NewTextHandler(nil, …)`) and forwarded raw
stdlib errors from `Handle` (`ctx.Err()`, `io.Writer.Write` error). There
was no cross-cutting convention for:

- identifying the **originating layer** of an error (kernel / core /
  service / public facade);
- separating a **wire-safe** message from a **log-only** detail;
- grouping errors by a stable identifier for dashboards and tests;
- mapping the error to a POSIX exit code or an HTTP/gRPC status.

The project's user asked explicitly for layered numeric codes inspired by
HTTP, paired with a public/private message split, usable both as exit
codes and as application-level error identifiers.

## Decision

Introduce `github.com/kitsunium/sdk/internal/kernel/errs` as the canonical
typed-error package. Consumers never import the internal package — they
receive `error` values from the SDK and introspect them via the read-only
accessors exposed in `github.com/kitsunium/sdk/pkg/v1/errs`.

Each SDK-produced `error` is backed by a `*errs.Error` carrying:

- `Code` — a layered integer (`1xxx kernel`, `2xxx core`, `3xxx service`,
  `4xxx public facade`), allocated per the registry below.
- `Reason` — a stable UPPER_SNAKE_CASE identifier (equal to the sentinel
  variable's name in its package, enforced by the AST audit test).
- `Public` — a wire-safe message (string literal, ≤120 runes, no newline).
  The AST audit refuses non-literal third arguments to `errs.Define`.
- `Private` — a detailed log-only message; may reference internal concepts.
- `Fields` — a closed scalar union (`String/Int/Bool/Float`). No raw `any`.
- optional per-error `HTTPStatus` and `ExitCode` overrides (defaults 500
  and 70 respectively — no layer-derived heuristic).

### Semantics

- **Origin wins on wrap.** `errs.Wrap(cause, params, fields...)` returns an
  `*Error` that inherits `cause`'s code/reason/public/private when `cause`
  is already an `*Error`. Only fields are appended. When `cause` is a
  stdlib error, `params` supply the values and the stdlib cause is
  preserved through `Unwrap` so `errors.Is(err, context.Canceled)` keeps
  working. **The `Code` describes the error's origin, not its observation
  surface** — an error born in `service/logger` and observed through
  `pkg/v1/logger` still carries a `31xx` code.
- **`Error()` returns a neutral stable form** `"[<code> <REASON>] <public>"`
  — never Private, never Fields. Safe to bubble across any boundary.
- **`errors.Is` keeps stdlib semantics.** For Code/Reason matching, use
  `errs.HasCode(err, code)` / `errs.HasReason(err, reason)` explicitly.
- **Private is diagnostic-only.** `errs.PrivateOf` is exposed in
  `pkg/v1/errs` so observability tooling can correlate a request-id header
  with the Private message in the log backend without re-logging the whole
  chain. It must never appear in HTTP/gRPC responses, error pages, or any
  user-facing surface.

## Registry

Authoritative code allocation. The AST audit in
`internal/kernel/errs/registry_audit_external_test.go` enforces code
uniqueness across the SDK.

| Range | Package | State | Codes used in this MR |
|---|---|---|---|
| 1000-1099 | `internal/kernel/errs` (emitter infrastructure package; the 1000-1099 range is documentary meta-codes only — no exported sentinels) | documentary | `CodeInvalidCode=1001`, `CodeInvalidReason=1002`, `CodeInvalidPublic=1003` (cited in panic messages, not exposed as runtime `*Error` sentinels) |
| 1100-1199 | `internal/core/logger/level` | reserved | — |
| 1200-1299 | `internal/kernel/buffer` | reserved | — |
| 1300-1399 | `internal/kernel/clock` | reserved | — |
| 2100-2199 | `internal/core/logger` | reserved | — |
| 2200-2299 | `internal/core/codec` | emitter | `CodeDuplicateRegistration=2201`, `CodeEmptyInput=2202`, `CodeTargetInvalid=2203`, `CodeValueInvalid=2204` |
| 3100-3199 | `internal/service/logger` | emitter | `CodeWriterNil=3101`, `CodeHandlerNil=3102`, `CodeCtxCancelled=3110`, `CodeWriteFailed=3120` |
| 3200-3299 | `internal/service/codec/<format>` | emitter | see codec subrange table below |
| 4100-4199 | `pkg/v1/logger` | emitter | `CodeWriterRequired=4101` |
| 4200-4299 | `pkg/v1/codec` + `pkg/v1/codec/baseenc` | emitter | `CodeUnknownFormat=4201`, `CodeCodecUnavailable=4202`, `CodeStreamingUnsupported=4203`, `CodeInvalidEncoding=4251`, `CodeDecodeFailed=4252` |
| ≥ 10000 | `pkg/v2+` | future | — |

### Codec service subranges (3200-3299)

| Range | Package | Codes in M1 |
|---|---|---|
| 3200-3209 | reserved for internal plumbing | — |
| 3210-3219 | `internal/service/codec/json` | `CodeMarshalFailed=3211`, `CodeUnmarshalFailed=3212` |
| 3220-3229 | `internal/service/codec/xml` | `CodeMarshalFailed=3221`, `CodeUnmarshalFailed=3222` |
| 3230-3239 | `internal/service/codec/yaml` (M2) | reserved |
| 3240-3249 | `internal/service/codec/toml` (M3) | reserved |
| 3250-3259 | `internal/service/codec/cbor` (M4) | reserved |
| 3260-3269 | `internal/service/codec/msgpack` (M4) | reserved |
| 3270-3279 | `internal/service/codec/csv` | `CodeMarshalFailed=3271`, `CodeUnmarshalFailed=3272`, `CodeValueInvalid=3273` |
| 3280-3284 | `internal/service/codec/asn1` | `CodeMarshalFailed=3281`, `CodeUnmarshalFailed=3282` |
| 3285-3289 | `internal/service/codec/pem` | `CodeMarshalFailed=3286`, `CodeUnmarshalFailed=3287`, `CodeValueInvalid=3288` |
| 3290-3299 | reserved for future baseenc-style service codecs | — |
| 3300-3309 | `internal/service/codec/ndjson` | `CodeMarshalFailed=3301`, `CodeUnmarshalFailed=3302`, `CodeValueInvalid=3303` |
| 3310-3319 | `internal/service/codec/hcl` (M5, optional) | reserved |
| 3320-3329 | `internal/service/codec/bson` (M5, optional) | reserved |
| 3330+     | schema-based codecs (protobuf / capnp / fb / avro) — deferred | reserved |

## Enforcement

- **Runtime.** `errs.Define` panics at `init` (factored through the
  testable helper `validateDefineArgs`) when `code < 1000`, `reason` is
  not SCREAMING_SNAKE, `public` is empty / `>120 runes` / contains a
  newline, or `private` is empty.
- **AST audit.** `TestAuditPublicIsStringLiteral`,
  `TestAuditReasonMatchesVarName`, and `TestAuditCodeUniqueness` walk
  every non-test `.go` file under `internal/` and `pkg/` and reject:
  a non-literal `public` argument, a `reason` that does not equal
  `screamingSnake(var name)`, or a duplicate `code` identifier.
- **ktn-linter.** The SDK-wide 148-rule policy is applied to every file
  in this MR (run `make sdk-lint`).

## `Field` contract

`FieldValue` is a closed union across `string / int64 / bool / float64`.
Fields exist for transport inside an `Error` and for **textual restitution
in logs/dumps** via `StringValue`. The consumer contract stops at
observation; strongly-typed reconstruction is not a design goal. This
keeps the central error type predictable, serialisable, and comparable in
tests without relying on `any`.

## Breaking changes

**4 breaking signature changes** (return type `T` → `(T, error)`):

- `internal/service/logger.NewTextHandler`
- `internal/service/logger.New`
- `pkg/v1/logger.NewText`
- `pkg/v1/logger.Default`

**1 breaking behaviour change**:

- `pkg/v1/logger.NewText` no longer defaults `cfg.Writer` to `os.Stderr`;
  `cfg.Writer == nil` returns `(nil, WriterRequired)`. Callers who want
  the previous convenience call `logger.Default()` (which supplies
  `os.Stderr` explicitly). **This is a deliberate product choice**:
  explicit construction prevents logs being silently redirected to stderr
  when a consumer forgets to set `Writer`.

## Framework version injection

`pkg/v1/logger.Version` is overridden at build time via:

```bash
go build -ldflags "-X github.com/kitsunium/sdk/pkg/v1/logger.Version=v0.1.0" ./...
```

`FrameworkVersion()` returns `"dev"` when `Version` is unset. Every
record emitted through `pkg/v1/logger.NewText` carries the version as a
`framework_version` attribute for observability correlation.

## Why not ...

- **Plain Go sentinels + `errors.Is`.** Lacks a stable machine-readable
  identifier (beyond the sentinel value) for dashboards/alerting, and has
  no public/private message split.
- **gRPC canonical status codes alone (0-16).** Too few identifiers, no
  layer provenance, no SDK-specific reasons.
- **Numeric codes without Reason.** Codes rot across refactors — gRPC/
  Google explicitly pair an enum with a stable `reason` string for this
  reason.
- **A single public `errs` package.** Risks callers forging SDK errors
  and couples them to the internal Field type's evolution.

## Deferred

- HTTP / gRPC adapters (`errs` only provides `HTTPStatus()` / `ExitCode()`
  today; middleware wiring comes later).
- `slog.LogValuer` / `AppendFields([]slog.Attr) []slog.Attr` for
  `log/slog` interop.
- Per-key Field redaction policy.
- Public `FieldKind` type + `Kind()` getter (once a consumer asks).
- Split `Wrap` into `Adopt` (stdlib cause) + `Enrich` (*Error cause)
  when two real usages exist.

## References

- Plan: [`.claude/plans/hidden-discovering-mist.md`](../../.claude/plans/hidden-discovering-mist.md)
- Context: [`.claude/contexts/sdk-errors-package.md`](../../.claude/contexts/sdk-errors-package.md)
- Google AIP-193: <https://google.aip.dev/193>
- RFC 9457 (Problem Details for HTTP APIs): <https://datatracker.ietf.org/doc/html/rfc9457>
- RFC 9110 (HTTP semantics): <https://datatracker.ietf.org/doc/html/rfc9110>
- sysexits(3): <https://man.openbsd.org/sysexits.3>
- Go stdlib errors: <https://pkg.go.dev/errors>
