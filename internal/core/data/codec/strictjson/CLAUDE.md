<!-- updated: 2026-10-03T00:00:00Z -->
# internal/core/data/codec/strictjson/

## Purpose

The error codes and the sentinels of `internal/service/data/codec/strictjson` — the strict JSON decoder (not a codec, ADR 0102) — and nothing else. They were declared in that service package until ADR 0160 §2 put every code in the core, at the path that mirrors the package emitting it; they moved with their values, reasons, public and private texts unchanged, so no rendering, no `errs.HasCode` match and no log query changed. The mechanism — encoding, decoding, the bounds, and the constructors that wrap a failure's detail around these codes — stays in the service package, which imports this one as `corestrictjson`.

Stdlib-only: this package imports `internal/kernel/errs` and nothing else.

## Surface

| File | Declares |
|---|---|
| `codes_gen.go` | 8 `errs.Code` constants, range `0.3.72.*`; 8 `errs.Define` sentinels, one per code — written by kit gen from `design/data/codec/strictjson.yaml` (ADR 0164) |
| `errors.go` | hand-written beside them: `exitDataErr`, `exitIOErr`, `exitSoftware`, `httpBadRequest`, `httpContentTooLarge`, `httpUnsupportedMediaType` |

## Error codes (range `0.3.72.*`)

| Code | Const | Sentinel | Reason |
|---|---|---|---|
| `0.3.72.1` | `CodeDocumentTooLarge` | `DocumentTooLarge` | `DOCUMENT_TOO_LARGE` |
| `0.3.72.2` | `CodeDocumentEmpty` | `DocumentEmpty` | `DOCUMENT_EMPTY` |
| `0.3.72.3` | `CodeDocumentMalformed` | `DocumentMalformed` | `DOCUMENT_MALFORMED` |
| `0.3.72.4` | `CodeMemberUnknown` | `MemberUnknown` | `MEMBER_UNKNOWN` |
| `0.3.72.5` | `CodeValueMismatched` | `ValueMismatched` | `VALUE_MISMATCHED` |
| `0.3.72.6` | `CodeMediaTypeUnsupported` | `MediaTypeUnsupported` | `MEDIA_TYPE_UNSUPPORTED` |
| `0.3.72.7` | `CodeDocumentUnreadable` | `DocumentUnreadable` | `DOCUMENT_UNREADABLE` |
| `0.3.72.8` | `CodeDecodeMisconfigured` | `DecodeMisconfigured` | `DECODE_MISCONFIGURED` |

Each sentinel carries an HTTP status and an exit code — 413 for `DocumentTooLarge`, 415 for `MediaTypeUnsupported`, 400 for the other refusals of the input, none (500) for `DecodeMisconfigured`; exit 65, 74 for `DocumentUnreadable`, 70 for `DecodeMisconfigured`. The statuses are int literals named after the `net/http` constant they equal: the core does not import `net/http` for a number. `TestRefusalsKeepTheirStatuses` pins them.

A `pkg/v1` facade that names these aliases this package, never the service one (ADR 0074).

## Do NOT

- Change a value to make it fit this directory. `LL = 3` records the layer that allocated the range, not the one that declares it (ADR 0160 §3); a renumbered code breaks every dashboard, alert rule and client that branches on it.
- Add encoding, decoding, a bound or a failure constructor here: that is mechanism, and it stays in `internal/service/data/codec/strictjson` (ADR 0160 §4).
- Import `net/http` for a status: `errs.WithHTTPStatus` takes an int.
- Declare a code outside `0.3.72.*`, the range `codeRangeOwners` gives this directory.

## Verification

```sh
bazel test --config=race //internal/core/data/codec/strictjson:strictjson_test
cd internal/core && GOWORK=off go test -race ./data/codec/strictjson/
```

`TestCodesKeepTheirValues` pins every code to its literal value and to the sentinel and reason bound to it. The range-ownership audit (`bazel test //internal/kernel/errs:errs_test`) judges this package against `codeRangeOwners`, through `//:audit_sources`.
