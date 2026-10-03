<!-- updated: 2026-10-03T00:00:00Z -->
# internal/core/data/codec/jsonpatch/

## Purpose

The error codes and the sentinels of `internal/service/data/codec/jsonpatch` — the JSON-difference package (RFC 6902; not a codec, ADR 0143) — and nothing else. They were declared in that service package until ADR 0160 §2 put every code in the core, at the path that mirrors the package emitting it; they moved with their values, reasons, public and private texts unchanged, so no rendering, no `errs.HasCode` match and no log query changed. The mechanism — encoding, decoding, the bounds, and the constructors that wrap a failure's detail around these codes — stays in the service package, which imports this one as `corejsonpatch`.

Stdlib-only: this package imports `internal/kernel/errs` and nothing else.

## Surface

| File | Declares |
|---|---|
| `codes.go` | 1 `errs.Code` constant, range `0.3.90.*` |
| `errors.go` | 1 `errs.Define` sentinel, one per code |

## Error codes (range `0.3.90.*`)

| Code | Const | Sentinel | Reason |
|---|---|---|---|
| `0.3.90.1` | `CodeNotJSON` | `NotJSON` | `NOT_JSON` |

`NotJSON` carries HTTP 400 and exit code 65 (EX_DATAERR). The 400 is an int literal: the core does not import `net/http` for a number, which is what kept `net/http` in every program that compared two documents. `TestNotJSONKeepsItsStatus` pins both.

A `pkg/v1` facade that names these aliases this package, never the service one (ADR 0074).

## Do NOT

- Change a value to make it fit this directory. `LL = 3` records the layer that allocated the range, not the one that declares it (ADR 0160 §3); a renumbered code breaks every dashboard, alert rule and client that branches on it.
- Add encoding, decoding, a bound or a failure constructor here: that is mechanism, and it stays in `internal/service/data/codec/jsonpatch` (ADR 0160 §4).
- Import `net/http` for a status: `errs.WithHTTPStatus` takes an int.
- Declare a code outside `0.3.90.*`, the range `codeRangeOwners` gives this directory.

## Verification

```sh
bazel test --config=race //internal/core/data/codec/jsonpatch:jsonpatch_test
cd internal/core && GOWORK=off go test -race ./data/codec/jsonpatch/
```

`TestCodesKeepTheirValues` pins every code to its literal value and to the sentinel and reason bound to it. The range-ownership audit (`bazel test //internal/kernel/errs:errs_test`) judges this package against `codeRangeOwners`, through `//:audit_sources`.
