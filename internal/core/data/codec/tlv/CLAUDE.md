<!-- updated: 2026-10-03T00:00:00Z -->
# internal/core/data/codec/tlv/

## Purpose

The error codes and the sentinels of `internal/service/data/codec/tlv` — the TLV codec — and nothing else. They were declared in that service package until ADR 0160 §2 put every code in the core, at the path that mirrors the package emitting it; they moved with their values, reasons, public and private texts unchanged, so no rendering, no `errs.HasCode` match and no log query changed. The mechanism — encoding, decoding, the bounds, and the constructors that wrap a failure's detail around these codes — stays in the service package, which imports this one as `coretlv`.

Stdlib-only: this package imports `internal/kernel/errs` and nothing else.

## Surface

| File | Declares |
|---|---|
| `codes.go` | 6 `errs.Code` constants, range `0.3.22.*` |
| `errors.go` | 6 `errs.Define` sentinels, one per code |

## Error codes (range `0.3.22.*`)

| Code | Const | Sentinel | Reason |
|---|---|---|---|
| `0.3.22.1` | `CodeTLVMarshalFailed` | `MarshalFailed` | `MARSHAL_FAILED` |
| `0.3.22.2` | `CodeTLVUnmarshalFailed` | `UnmarshalFailed` | `UNMARSHAL_FAILED` |
| `0.3.22.3` | `CodeTLVUnsupportedType` | `UnsupportedType` | `UNSUPPORTED_TYPE` |
| `0.3.22.4` | `CodeTLVDepthExceeded` | `DepthExceeded` | `DEPTH_EXCEEDED` |
| `0.3.22.5` | `CodeTLVSizeExceeded` | `SizeExceeded` | `SIZE_EXCEEDED` |
| `0.3.22.6` | `CodeTLVTruncated` | `Truncated` | `TRUNCATED` |

A `pkg/v1` facade that names these aliases this package, never the service one (ADR 0074).

## Do NOT

- Change a value to make it fit this directory. `LL = 3` records the layer that allocated the range, not the one that declares it (ADR 0160 §3); a renumbered code breaks every dashboard, alert rule and client that branches on it.
- Add encoding, decoding, a bound or a failure constructor here: that is mechanism, and it stays in `internal/service/data/codec/tlv` (ADR 0160 §4).
- Import `net/http` for a status: `errs.WithHTTPStatus` takes an int.
- Declare a code outside `0.3.22.*`, the range `codeRangeOwners` gives this directory.

## Verification

```sh
bazel test --config=race //internal/core/data/codec/tlv:tlv_test
cd internal/core && GOWORK=off go test -race ./data/codec/tlv/
```

`TestCodesKeepTheirValues` pins every code to its literal value and to the sentinel and reason bound to it. The range-ownership audit (`bazel test //internal/kernel/errs:errs_test`) judges this package against `codeRangeOwners`, through `//:audit_sources`.
