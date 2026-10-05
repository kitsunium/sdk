<!-- updated: 2026-10-03T00:00:00Z -->
# internal/core/data/codec/multipart/

## Purpose

The error codes and the sentinels of `internal/service/data/codec/multipart` — the multipart/form-data codec — and nothing else. They were declared in that service package until ADR 0160 §2 put every code in the core, at the path that mirrors the package emitting it; they moved with their values, reasons, public and private texts unchanged, so no rendering, no `errs.HasCode` match and no log query changed. The mechanism — encoding, decoding, the bounds, and the constructors that wrap a failure's detail around these codes — stays in the service package, which imports this one as `coremultipart`.

Stdlib-only: this package imports `internal/kernel/errs` and nothing else.

## Surface

| File | Declares |
|---|---|
| `codes_gen.go` | 6 `errs.Code` constants, range `0.3.41.*`; 6 `errs.Define` sentinels, one per code — written by kit gen from `design/data/codec/multipart.yaml` (ADR 0164) |
| `doc.go` | the package comment, kept when its codes and sentinels moved to the design (ADR 0164) |

## Error codes (range `0.3.41.*`)

| Code | Const | Sentinel | Reason |
|---|---|---|---|
| `0.3.41.1` | `CodeMultipartMarshalFailed` | `MarshalFailed` | `MARSHAL_FAILED` |
| `0.3.41.2` | `CodeMultipartUnmarshalFailed` | `UnmarshalFailed` | `UNMARSHAL_FAILED` |
| `0.3.41.3` | `CodeMultipartValueInvalid` | `ValueInvalid` | `VALUE_INVALID` |
| `0.3.41.4` | `CodeMultipartBoundaryInvalid` | `BoundaryInvalid` | `BOUNDARY_INVALID` |
| `0.3.41.5` | `CodeMultipartLimitExceeded` | `LimitExceeded` | `LIMIT_EXCEEDED` |
| `0.3.41.6` | `CodeMultipartLimitsInvalid` | `LimitsInvalid` | `LIMITS_INVALID` |

A `pkg/v1` facade that names these aliases this package, never the service one (ADR 0074).

## Do NOT

- Change a value to make it fit this directory. `LL = 3` records the layer that allocated the range, not the one that declares it (ADR 0160 §3); a renumbered code breaks every dashboard, alert rule and client that branches on it.
- Add encoding, decoding, a bound or a failure constructor here: that is mechanism, and it stays in `internal/service/data/codec/multipart` (ADR 0160 §4).
- Import `net/http` for a status: `errs.WithHTTPStatus` takes an int.
- Declare a code outside `0.3.41.*`, the range `codeRangeOwners` gives this directory.

## Verification

```sh
bazel test --config=race //internal/core/data/codec/multipart:multipart_test
cd internal/core && GOWORK=off go test -race ./data/codec/multipart/
```

`TestCodesKeepTheirValues` pins every code to its literal value and to the sentinel and reason bound to it. The range-ownership audit (`bazel test //internal/kernel/errs:errs_test`) judges this package against `codeRangeOwners`, through `//:audit_sources`.
