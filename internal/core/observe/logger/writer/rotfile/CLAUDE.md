<!-- updated: 2026-10-03T12:00:00Z -->
# internal/core/observe/logger/writer/rotfile/

## Purpose

The codes and sentinels of the rotating-file log writer, whose engine is
`internal/service/observe/logger/writer/rotfile`. The range `0.3.27.*` was allocated to that engine
(ADR 0005) and is declared here since ADR 0160 §2, which puts every code of a
service package in the core package at the same path: the engine returns these
sentinels and declares none, and `pkg/v1` aliases them from here when it
publishes them (ADR 0074).

Nothing else lives here. It has no port of its own: the writer registers a `Factory` in `internal/core/observe/logger/writer`, whose product is the logger's `Sink`.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `CodeRotFileOpenFailed`, `CodeRotFileRotateFailed`, `CodeRotFileWriteFailed`, `CodeRotFileDecodeFailed` | `errs.Code` constants | one per sentinel, literal values |
| `RotFileOpenFailed`, `RotFileRotateFailed`, `RotFileWriteFailed`, `RotFileDecodeFailed` | `*errs.Error` sentinels | built by `errs.Define`; match with `errs.HasCode` or `errors.Is` |

## Error codes — range 0.3.27.*

| Code | Constant | Sentinel | Reason | Exit / status |
|---|---|---|---|---|
| 0.3.27.1 | `CodeRotFileOpenFailed` | `RotFileOpenFailed` | `ROT_FILE_OPEN_FAILED` | 74 (EX_IOERR) |
| 0.3.27.2 | `CodeRotFileRotateFailed` | `RotFileRotateFailed` | `ROT_FILE_ROTATE_FAILED` | 74 (EX_IOERR) |
| 0.3.27.3 | `CodeRotFileWriteFailed` | `RotFileWriteFailed` | `ROT_FILE_WRITE_FAILED` | 74 (EX_IOERR) |
| 0.3.27.4 | `CodeRotFileDecodeFailed` | `RotFileDecodeFailed` | `ROT_FILE_DECODE_FAILED` | 78 (EX_CONFIG) |

## Do NOT

- Renumber a code to match this directory. `LL = 3` records the layer that
  ALLOCATED the range, not the one that declares it, and a consumer branches on
  the value (ADR 0160 §3); `codeRangeOwners` maps the unchanged key here.
- Declare a code back in the engine: a service package declares none
  (ADR 0160 §2).
- Add behaviour here. What the writer does is the engine's; this package
  carries only what a caller matches a failure against.

## Verification

```sh
cd internal/core && GOWORK=off go test -race -count=1 ./observe/logger/writer/rotfile/
bazel test //internal/core/observe/logger/writer/rotfile:rotfile_test
bazel test //internal/kernel/errs:errs_test   # range ownership + uniqueness audits
```
