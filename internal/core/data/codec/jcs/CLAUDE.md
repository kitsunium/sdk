<!-- updated: 2026-11-03T00:00:00Z -->
# internal/core/data/codec/jcs/

## Purpose

The error codes and the sentinels of `internal/service/data/codec/jcs` — RFC 8785 canonicalization (not a codec Format; ADR 0102 amendment of 2026-11-03) — and nothing else (ADR 0160 §2). The mechanism stays in the service package, which imports this one as `corejcs`.

Stdlib-only: this package imports `internal/kernel/errs` and nothing else.

## Surface

| File | Declares |
|---|---|
| `gen_codes.go` | 3 `errs.Code` constants, range `0.3.102.*`; 3 `errs.Define` sentinels, one per code — written by kit gen from `design/data/codec/jcs.yaml` (ADR 0164) |

## Error codes (range `0.3.102.*`)

| Code | Const | Sentinel | Reason |
|---|---|---|---|
| `0.3.102.1` | `CodeJCSNotJSON` | `NotJSON` | `NOT_JSON` — not exactly one I-JSON value; 400, exit 65 |
| `0.3.102.2` | `CodeJCSNumberInvalid` | `NumberInvalid` | `NUMBER_INVALID` — a number past the largest finite double; 400, exit 65 |
| `0.3.102.3` | `CodeJCSMarshalFailed` | `MarshalFailed` | `MARSHAL_FAILED` — json/v2 refused the value `Marshal` was given |

No Public and no Private carries a byte of the document: a refusal names the offset only.

## Do NOT

- Change a value: a renumbered code breaks every client that branches on it.
- Add canonicalization here: it is mechanism, and stays in the service package (ADR 0160 §4).
