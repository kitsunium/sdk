<!-- updated: 2026-10-03T00:00:00Z -->
# internal/core/data/codec/form/

## Purpose

The error codes and the sentinels of `internal/service/data/codec/form` — the application/x-www-form-urlencoded codec — and nothing else. They were declared in that service package until ADR 0160 §2 put every code in the core, at the path that mirrors the package emitting it; they moved with their values, reasons, public and private texts unchanged, so no rendering, no `errs.HasCode` match and no log query changed. The mechanism — encoding, decoding, the bounds, and the constructors that wrap a failure's detail around these codes — stays in the service package, which imports this one as `coreform`.

Stdlib-only: this package imports `internal/kernel/errs` and nothing else.

## Surface

| File | Declares |
|---|---|
| `codes.go` | 3 `errs.Code` constants, range `0.3.40.*` |
| `errors.go` | 3 `errs.Define` sentinels, one per code |

## Error codes (range `0.3.40.*`)

| Code | Const | Sentinel | Reason |
|---|---|---|---|
| `0.3.40.1` | `CodeFormValueInvalid` | `ValueInvalid` | `VALUE_INVALID` |
| `0.3.40.2` | `CodeFormUnmarshalFailed` | `UnmarshalFailed` | `UNMARSHAL_FAILED` |
| `0.3.40.3` | `CodeFormMultiValue` | `MultiValue` | `MULTI_VALUE` |

There is deliberately no `MarshalFailed`: past the codec's argument-shape gate, percent-escaping a map of strings is a total function, and a sentinel no test can provoke would be a lie in the registry.

A `pkg/v1` facade that names these aliases this package, never the service one (ADR 0074).

## Do NOT

- Change a value to make it fit this directory. `LL = 3` records the layer that allocated the range, not the one that declares it (ADR 0160 §3); a renumbered code breaks every dashboard, alert rule and client that branches on it.
- Add encoding, decoding, a bound or a failure constructor here: that is mechanism, and it stays in `internal/service/data/codec/form` (ADR 0160 §4).
- Import `net/http` for a status: `errs.WithHTTPStatus` takes an int.
- Declare a code outside `0.3.40.*`, the range `codeRangeOwners` gives this directory.

## Verification

```sh
bazel test --config=race //internal/core/data/codec/form:form_test
cd internal/core && GOWORK=off go test -race ./data/codec/form/
```

`TestCodesKeepTheirValues` pins every code to its literal value and to the sentinel and reason bound to it. The range-ownership audit (`bazel test //internal/kernel/errs:errs_test`) judges this package against `codeRangeOwners`, through `//:audit_sources`.
