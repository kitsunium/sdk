<!-- updated: 2026-11-09T00:00:00Z -->
# internal/core/net/http2/

## Purpose

The **contract** of the SDK's HTTP/2 codec (ADR 0221): the values a frame
reader, the connection state machine and `net/server` read, and the codes its
refusals carry. The engine — the frame layer, HPACK and the connection state
machine — is `internal/service/net/http2`, at the mirrored path (ADR 0160).

Range `0.3.109.*` (`design/sdk.yaml` `codes.ranges`; ADR 0221). Imports:
`internal/kernel/errs`.

**Generated from the design** (ADR 0163): the codes and sentinels in
`gen_codes.go`, the pins in `gen_api_test.go`, the package comment in `doc.go`,
all from `design/net/http2.yaml`. Change the design, then `kit gen`, then
`make api` — never the generated files.

## Contents

| File | What it is |
|---|---|
| `values.go` | `FrameHeaderLen`, `FrameValue`, the frame types and flags, `ErrCode` (RFC 9113 §7, with `String`), the settings identifiers and `SettingValue`, `LimitsValue` and its defaults, with `Resolve` |
| `error_type.go` | `ErrorType` (#966): one per RFC 9113 code past NO_ERROR, numbered as it, then `ErrorTypeHeaderList`, `ErrorTypePingTimeout`, `ErrorTypeOther`; `ErrorTypes` bounds a table indexed by one; `String`/`MarshalText` in lower snake case; `ErrorTypeOf(code)`; `ErrorCountValue`, a row of State's `HTTP2Errors` |
| `doc.go`, `gen_codes.go`, `gen_api_test.go` | written by kit |
| `limits_external_test.go` | what `Resolve` defaults and what it refuses |

## Error codes (range `0.3.109.*`)

| Code | Sentinel | Reason |
|---|---|---|
| `0.3.109.1` | `FrameMalformed` | `FRAME_MALFORMED` — a frame that breaks its layout |
| `0.3.109.2` | `HeadersTooLarge` | `HEADERS_TOO_LARGE` — a block past the header list ceiling, or holding more fields than a request value has room for; http 431 |
| `0.3.109.3` | `CompressionFailed` | `COMPRESSION_FAILED` — a block HPACK cannot decode: a bad index, integer, string or size update; the table is undefined and the connection ends |
| `0.3.109.4` | `FieldMalformed` | `FIELD_MALFORMED` — RFC 9113 §8.2.1: the block was decoded to its end, the table intact, only the stream is refused |
| `0.3.109.5` | `LimitsRefused` | `LIMITS_REFUSED` — a `LimitsValue` out of its range; the `reason` field names it |

`ErrCode` is not one of them: it is the wire's vocabulary, what `RST_STREAM`
and `GOAWAY` carry.

## Bounds

`LimitsValue` holds ADR 0195 §D3's numbers: `MaxStreams` 100, `ReceivePerStream`
256 KiB, `ReceivePerConn` four streams' worth, `MaxFrame` 16 KiB,
`HeaderTable` 4 KiB (`NoDynamicTable` -1 advertises none), `MaxHeaderList`
8 192, RFC 9113 §6.5.2's measure. A zero field is its default (ADR 0031).

## Do NOT

- Change a code's value, or give a wire `ErrCode` the meaning of a sentinel.
- Put the wire format here: the frame layer and HPACK are the engine's.
