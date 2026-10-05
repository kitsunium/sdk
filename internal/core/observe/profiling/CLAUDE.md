<!-- updated: 2026-10-05T00:00:00Z -->
# internal/core/observe/profiling/

## Purpose

The profiling domain's values and codes (ADR 0121), given a core package by
ADR 0160 §1 so that every service domain has one: a decoded profile, a profile
folded onto owners, a goroutine as the runtime's dump describes it, and the
seven sentinels the engine returns. The engine — capturing the CPU and the
heap, decoding the pprof format with the standard library, folding, reading
and grouping goroutine dumps — is `internal/service/observe/profiling`; the
facade, `pkg/v1/observe/profiling`, aliases the values and the sentinels from
here (ADR 0074).

Stdlib only, plus `kernel/errs`. Code range `0.3.89.*`, allocated to the engine
by ADR 0121 and declared here since ADR 0160 §2 with its values unchanged.

## Surface

| File | Symbols |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `decl_gen.go` | written by kit gen from the design (ADR 0170): the declarations of `FoldedValue`, `OwnerCostValue`, `FunctionCostValue`, `FlameNodeValue`, `GoroutineValue`, `GoroutineGroupValue`, `ProfileValue`, `SampleTypeValue`, `SampleValue` and `FrameValue` — each struct with every field, unexported ones included. Their methods, constructors and helpers stay hand-written, in the files this document names |
| `folded.go` | `FoldedValue` (owners, top functions, flame, total, unattributed), `OwnerCostValue`, `FunctionCostValue`, `FlameNodeValue`, `FlameRoot` |
| `codes_gen.go` | the seven codes and sentinels below — written by kit gen from `design/observe/profiling.yaml` (ADR 0164) |
| `errors.go` | hand-written beside them: `httpBadRequest`, `httpConflict`, `httpUnavailable` |

What stays in the engine, and why: `FoldConfig` and `GroupConfig` are the
parameters of `Fold` and `GroupGoroutines`, and an engine's configuration
lives with the engine (`MeterConfig` and `TracerConfig` do the same), with the
`Default*` values their zero fields take; `MaxCPUWindow`, `MaxProfileBytes`
and `MaxFrames` are the bounds of the capture and of the decoder, which are
mechanisms. The unexported helpers that used to be methods of these values —
the sample-type lookup and the default sample type `Fold` uses, the bracket
reader of a dump header — became functions of the engine: a method declared
here would have joined the public method set of every alias.

## Error codes — range 0.3.89.*

| Code | Constant | Sentinel | Reason | HTTP |
|---|---|---|---|---|
| 0.3.89.1 | `CodeWindowInvalid` | `WindowInvalid` | `WINDOW_INVALID` | 400 — the window is the caller's to fix |
| 0.3.89.2 | `CodeProfilerBusy` | `ProfilerBusy` | `PROFILER_BUSY` | 409 — the runtime's one CPU profiler is taken |
| 0.3.89.3 | `CodeCaptureCanceled` | `CaptureCanceled` | `CAPTURE_CANCELED` | 503 — the caller gave up |
| 0.3.89.4 | `CodeCaptureFailed` | `CaptureFailed` | `CAPTURE_FAILED` | — |
| 0.3.89.5 | `CodeProfileMalformed` | `ProfileMalformed` | `PROFILE_MALFORMED` | — |
| 0.3.89.6 | `CodeProfileTooLarge` | `ProfileTooLarge` | `PROFILE_TOO_LARGE` | — |
| 0.3.89.7 | `CodeSampleTypeMissing` | `SampleTypeMissing` | `SAMPLE_TYPE_MISSING` | — |

No refusal quotes the profile or the dump it refused: both describe the
process's code, its files and what its goroutines were doing.

## Why there is no port

ADR 0160 §1 asks for the ports a second implementation or a test double needs.
There is one engine — the runtime's profilers — and the one thing a caller
varies, whose work a sample was, is a FUNCTION the caller passes to `Fold`
(`FoldConfig.Attribute`), not a contract the SDK declares (ADR 0121 §D1). The
framework's developer tools call the functions directly and need no double. A
port would be a surface somebody maintains for an implementation nobody has.

## Do NOT

- Add a method to a value here. The facade aliases them, so a method joins the
  public surface; an engine helper is a function of the engine.
- Move the bounds or the configs here: they are the engine's (above).
- Renumber a code to match this directory: `LL = 3` records the layer that
  allocated the range (ADR 0160 §3).
- Quote a profile or a dump in an error.

## Verification

```sh
cd internal/core && GOWORK=off go test -race -count=1 ./observe/profiling/
bazel test //internal/core/observe/profiling:profiling_test
bazel test //internal/kernel/errs:errs_test   # range ownership + uniqueness audits
```

`TestEverySentinelCarriesItsCode` pins the seven values and the three HTTP
statuses; the behaviour behind them is tested in the engine.
