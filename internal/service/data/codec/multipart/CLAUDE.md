<!-- updated: 2026-11-03T22:00:00Z -->
# internal/service/data/codec/multipart/

The reference half of this file moved, unchanged and under the same section titles, to
`internal/service/data/codec/multipart/NOTES.md` (ADR 0253): read the section a change touches.
It holds:
§The boundary problem; §Memory bounds (ADR 0031).

## Purpose

RFC 7578 `multipart/form-data` — the shape every file upload and HTML form
post arrives in. `mime/multipart` is stdlib, so this codec adds **zero
dependencies**. Its `Reader` parses every body; the encoder frames what it
writes itself, the bytes `mime/multipart.Writer` writes (#907, §Encoding cost).

Unlike every other codec in this tree, the format is a **container**, not a
value serialisation: it frames a list of named sections, each with its own
headers. Its native Go shape is therefore `FormValue` (a delimiter plus
`[]PartValue`), and it implements `core/data/codec.StreamingCodec` because the
format exists to *not* be loaded whole.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"multipart"` |
| `MIMETypes()`    | `multipart/form-data` |
| `Extensions()`   | none — see §Extensions below |
| Constructor      | `New() codec.Codec` · `NewWithLimits(LimitsConfig) (codec.Codec, error)` |
| Streaming        | **yes** — `NewEncoder` writes one part per `Encode`, `NewDecoder` reads one part per `Decode`; neither holds more than a single part body |
| Appender         | yes (`Append(dst, v) ([]byte, error)`) — prior contents preserved on failure |
| Value types      | `FormValue{Boundary, Parts}` · `PartValue{Name, FileName, ContentType, Data}` |
| Extensions (Go)  | `BoundaryCodec` (boundary-explicit constructors) · `BoundaryProvider` (read the generated delimiter off an `Encoder`) |
| Helpers          | `Boundary(data) (string, error)` · `ContentType(data) (string, error)` |

`Marshal` accepts `FormValue` / `*FormValue` / `[]PartValue` / `PartValue` /
`*PartValue` natively. **Any other value** takes the *JSON-mediated* shape: a
single part named `JSONPartName` (`"_json"`) with `Content-Type:
application/json` carrying `encoding/json`'s output. `Unmarshal` reverses it —
a `*FormValue` target gets the whole container, any other non-nil pointer gets
the `_json` part decoded into it. `baseenc` is the in-tree precedent for a
JSON-mediated pipeline; doing it *inside* the codec is what lets
`codec.Marshal(codec.Multipart, anyValue)` hold without adding a seventh entry to
`pkg/v1/data/codec/promote.go`'s constrained-codec table.

## Header values are refused, never repaired

`Name`, `FileName` and `ContentType` are written into the part's header block,
and the encoder writes header values **verbatim**, as
`mime/multipart.Writer.CreatePart` does: a CR
or LF inside one ends the line, and the rest becomes header lines — or, after an
empty line, body — the caller never wrote. The encoder refuses a CR, LF or NUL
in any of the three with `VALUE_INVALID` **before** it writes a byte of the part,
naming the field (`field` = `Name` / `FileName` / `ContentType`) and never the
value — ADR 0064's stance on mail headers. It deliberately does not do what
`mime/multipart`'s own escaper does (percent-encode CR and LF): that is a
repair, delivering a filename the caller never supplied while reporting
success. Quotes and backslashes are still escaped inside the quoted parameters
(`appendQuoted`), and UTF-8 is written raw (RFC 7578 §4.2). Pinned by
`TestEncoderRefusesHeaderInjection` and `TestEncodedHeadersReparseWithTheStdlib`,
which requires the stdlib reader to read back exactly the header block asked for.

## Encoding cost: the framing is written, not formatted (#907)

`mime/multipart.Writer.CreatePart` took a `textproto.MIMEHeader` — a map built
per part — sorted its keys and formatted every line through `fmt` into a fresh
`bytes.Buffer`: a four-field text form cost 69 allocations to Marshal. The
encoder now appends each part's delimiter line and header block
(`appendHead`) into one buffer it keeps for its lifetime, then writes it and
the body: the same bytes, Content-Disposition before Content-Type as the
writer's sorted keys put them, and the closing `\r\n--<boundary>--\r\n` its
`Close` wrote. `TestMarshalWritesWhatTheStdlibWriterWrites` holds every header
shape — bare field, file with a media type, quotes and backslashes, an empty
body, no part, a generated boundary — byte for byte to `mime/multipart.Writer`.
`codec_integration_test.go` gives multipart the race-off alloc budget the other
codecs have: three allocations per part on Marshal, Append and the stream
encoder (`BENCH.md` §#907).

## A filename is reduced by one rule, on every OS

The decoder reads the `Content-Disposition` itself (`disposition.go`) instead of
calling `(*mime/multipart.Part).FileName`, because that method passes the
parameter through `filepath.Base` — and `filepath.Base` is the HOST's: it splits
on `\` on Windows and not on Unix. The same body decoded `q"uo\te.txt` to
`te.txt` on Windows and kept it whole on Linux, and a full path an old browser
sent (`C:\Users\ada\cv.pdf`) came back stripped on one platform and verbatim on
the other. Found by the first Windows run of the whole suite (ADR 0095).

`lastElement` is the rule, and it does not depend on where it runs: trailing
separators are dropped (`dir/` names `dir`, as `filepath.Base` has it), then
everything after the last `/` OR `\` is kept. RFC 7578 §4.2 has the receiver
drop the directory information a sender may include, and the sender writes it
with ITS separator — so both separate, everywhere. Nothing else is removed: a
colon is an ordinary filename octet on Unix, and `.` / `..` come back as sent,
so a caller turning `FileName` into a path still validates it. A value made of
separators alone yields `""`, what a part with no usable filename already
carries. Pinned by `TestAFileNameKeepsOnlyItsLastPathElementOnEveryOS`,
mutation-checked against the stdlib call; `TestEncodedHeadersReparseWithTheStdlib`
reads the raw parameter for the same reason, since it is about what the ENCODER
wrote.

## Error codes (range `0.3.41.*`)

Declared in `internal/core/data/codec/multipart` — `codes.go` and `errors.go` — and used
here as `coremultipart.<Var>` (ADR 0160 §2: a code lives in the core at the path that
mirrors the package emitting it). The values, reasons and texts are the ones this
package always emitted; only the declaration moved.

| Code | Var | Trigger |
|---|---|---|
| `0.3.41.1` | `MarshalFailed`    | `mime/multipart` failed writing a part header/body/closing delimiter, or `encoding/json` rejected the value on the JSON-mediated path |
| `0.3.41.2` | `UnmarshalFailed`  | malformed part header, truncated body, a part with no form-data name (RFC 7578 §4.2 — the encoder refuses to write one, so it is refused on the way in too), non-JSON `_json` part, or a body with no `_json` part when the target is not a `*FormValue` |
| `0.3.41.3` | `ValueInvalid`     | `PartValue` with no `Name`; a CR, LF or NUL in `Name` / `FileName` / `ContentType` (field `field` names which, never the value); nil `*FormValue` / `*PartValue` (a decode target that is not a non-nil pointer is the family's `TARGET_INVALID` since #1101) |
| `0.3.41.4` | `BoundaryInvalid`  | delimiter not recoverable from the body, or a caller-supplied boundary outside RFC 2046 (1–70 bchars, no trailing space) |
| `0.3.41.5` | `LimitExceeded`    | `MaxPartBytes` / `MaxParts` / `MaxTotalBytes` crossed — fields carry `knob`, `bound`, `got` |
| `0.3.41.6` | `LimitsInvalid`    | a negative `LimitsConfig` field (ADR 0031 refusal) — field carries `knob` |

The range is allocated to this package in `codeRangeOwners`
(`design/sdk.yaml`'s `codes.ranges`, which kit writes into `internal/kernel/errs/gen_ranges_test.go` (ADR 0164), ADR 0035).

## Extensions

`Extensions()` returns nothing, deliberately. `multipart/form-data` is a
transport container with no registered file suffix; inventing one (`.multipart`)
would make `codec.FromExtension` resolve a name no producer emits. The MIME
alias is registered normally, and because the registry normalises MIME keys, a
real header — boundary parameter and all — still resolves:
`codec.FromMIME("multipart/form-data; boundary=----X")` → `Format("multipart")`.
Note the irony that closes the loop on §The boundary problem: the *facade* can
read the boundary out of the header it was handed, but the *codec* it dispatches
to never sees it.

## Not covered (and why)

- **Byte-streaming *inside* one part.** `codec.Decoder.Decode(v any)` has
  nowhere to hand back an `io.Reader` the caller must drain before the next
  call, so a part body is materialised whole under `MaxPartBytes`. Streaming is
  between parts, which is where the part *count* problem lives; a consumer
  moving objects larger than the ceiling raises it with `NewWithLimits`, or
  uses `mime/multipart` directly. Expressing it would need a `Decode` contract
  the domain does not have.
- **`multipart/mixed`, `multipart/related`, `multipart/byteranges`.** Same
  RFC 2046 framing, different `Content-Disposition` semantics. Only `form-data`
  is registered; the other subtypes would each need their own Format name.
- **`Content-Transfer-Encoding`.** RFC 7578 §4.7 discourages it and browsers do
  not emit it. Part bodies travel verbatim.
- **Preamble / epilogue.** Never written; on read, a preamble is skipped by the
  scan but its content is discarded rather than surfaced.

## Do NOT

- Reach for `mime/multipart` directly from another package in this tree —
  cross-codec composition belongs in `pkg/v1/data/codec` or in the caller.
- Add a file extension to make `FromExtension` "work". See §Extensions.
- Percent-encode or strip a CR / LF / NUL in a part header value to make it
  "fit". It is refused. See §Header values.
- Let a zero `LimitsConfig` field mean "unlimited". See §Memory bounds.
- Assume `Boundary(data)` is authoritative. It is a recovery. See §2 above.
- Trust `More()` alone to mean "no error happened" — it answers true once for a
  parked failure precisely so `Decode` gets to report it.

## Verification

```
bazel test --config=race //internal/service/data/codec/multipart:multipart_test
```

Locally, without Bazel:

```
cd internal/service && GOWORK=off go test -race ./data/codec/multipart/
```

## Fuzz (#897)

`fuzz_external_test.go` — `FuzzUnmarshal`: over a body framed by a boundary the input picks: any body decodes without a panic; a decoded body was framed by the boundary `Boundary` sniffs from it (sniff and confirm agree), and its parts re-encode, read back equal and re-encode to the same bytes. The seed corpus runs with the package's tests; `scripts/ci/codec-fuzz.sh` (nightly, `codec-fuzz.yml`) explores it.
