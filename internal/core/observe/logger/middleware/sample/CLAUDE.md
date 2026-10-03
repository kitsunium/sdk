<!-- updated: 2026-10-03T12:00:00Z -->
# internal/core/observe/logger/middleware/sample/

## Purpose

The codes and sentinels of the logger's sampling middleware, whose engine is
`internal/service/observe/logger/middleware/sample`. The range `0.3.20.*` was allocated to that engine
(ADR 0005) and is declared here since ADR 0160 §2, which puts every code of a
service package in the core package at the same path: the engine returns these
sentinels and declares none, and `pkg/v1` aliases them from here when it
publishes them (ADR 0074).

Nothing else lives here. It has no port of its own: the middleware is a `Sink` of `internal/core/observe/logger`, decorating another one.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `CodeSampleRateInvalid`, `CodeSampleDownstreamNil` | `errs.Code` constants | one per sentinel, literal values |
| `RateInvalid`, `DownstreamNil` | `*errs.Error` sentinels | built by `errs.Define`; match with `errs.HasCode` or `errors.Is` |

## Error codes — range 0.3.20.*

| Code | Constant | Sentinel | Reason | Exit / status |
|---|---|---|---|---|
| 0.3.20.1 | `CodeSampleRateInvalid` | `RateInvalid` | `SAMPLE_RATE_INVALID` | — |
| 0.3.20.2 | `CodeSampleDownstreamNil` | `DownstreamNil` | `SAMPLE_DOWNSTREAM_NIL` | — |

## Do NOT

- Renumber a code to match this directory. `LL = 3` records the layer that
  ALLOCATED the range, not the one that declares it, and a consumer branches on
  the value (ADR 0160 §3); `codeRangeOwners` maps the unchanged key here.
- Declare a code back in the engine: a service package declares none
  (ADR 0160 §2).
- Add behaviour here. What the sink does is the engine's; this package
  carries only what a caller matches a failure against.

## Verification

```sh
cd internal/core && GOWORK=off go vet ./observe/logger/middleware/sample/
bazel build //internal/core/observe/logger/middleware/sample
bazel test //internal/kernel/errs:errs_test   # range ownership + uniqueness audits
```
