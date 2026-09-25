# ADR 0102 — a document somebody else wrote is decoded one way, or not at all

- **Status**: Accepted
- **Date**: 2026-09-25
- **Deciders**: SDK maintainers
- **Related**: [ADR 0003](0003-sdk-codec-package.md) (the universal codec), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a zero bound), [ADR 0029](0029-sdk-net-domain.md) (the HTTP adapter), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) (the code range)

## Context

The codec's `json` format is `encoding/json` with its defaults, which is right
for data this program wrote. For a document somebody else wrote — a request
body, above all — it reads one document two ways, and never says so. Measured
on go1.27.1 against a struct whose fields are tagged `name` (a string) and
`price` (an `int32`):

| document | `json.Unmarshal` |
|---|---|
| `{"name":"a","name":"b"}` | accepted, `Name = "b"` — the last duplicate wins |
| `{"NAME":"x"}` | accepted, `Name = "x"` — members match fields case-insensitively |
| `{"name":"x","admin":true}` | accepted, `admin` silently dropped |
| `{"name":"\xff"}` | accepted, the byte silently becomes U+FFFD |
| `{"name":"x"} garbage`, through `json.NewDecoder` — the codec's streaming path | the first value decoded, the garbage left unread |

Each is a way for this program and whatever parsed the document first — a
proxy, a signature check, an audit log — to disagree about what it said, and
none of them fails. Nothing bounds the size either. And the refusals that do
exist quote the input: `json: cannot unmarshal number 3000000000 into Go struct
field item.price of type int32`, `invalid character 'x' looking for beginning
of value`.

A downstream framework wrote the strict decoder twice — once for its
endpoints, once for its developer console — on `encoding/json/v2`, with a size
bound through `http.MaxBytesReader`, an empty-body check, a media-type check
answering 415, and error text it wrote itself so as not to echo values.

## Decision

A new service package, `internal/service/codec/strictjson`, published as
`pkg/v1/codec/strictjson`. Code range `0.3.72.*` (`0x00_03_48_*`), eight codes,
each carrying its HTTP status.

### D1 — `encoding/json/v2`, whose defaults refuse most of the table

`Decode(r, v, maxBytes)` is `jsonv2.UnmarshalRead` with
`RejectUnknownMembers(true)`. v2's own defaults already refuse a duplicate
name, a case-only match, invalid UTF-8 and anything after the one value; the
option adds unknown members. Go 1.27 ships `encoding/json/v2` without an
experiment flag, which is what `service/codec/json`'s note calls the lever "ADR
gated": this is that ADR, and it pulls the lever for this decoder only.
`codec.Unmarshal(codec.JSON, …)` keeps meaning what it has always meant.

### D2 — a bound on reading, not a check afterwards

The decoder reads through a reader that hands out the bound plus ONE byte and
then reports the end of input, so a client that never stops sending costs
`maxBytes + 1` bytes. A document longer than the bound is `DocumentTooLarge`
whatever the decoder made of the part it read: the part is a truncation, not
the document. The reader's verdict — too large, then a failed source, then
zero bytes — is taken before the decoder's, because each of the three makes
the decoder's conclusion an artefact. A non-positive bound is refused
(`DecodeMisconfigured`): its readings, "unlimited" and "nothing", are
opposites and the first is a memory-exhaustion bug (ADR 0031).

### D3 — no refusal repeats the document

Eight fixed sentences. The decoder's own error is NOT kept in the chain, since
its text quotes the offending member name and, through a field's own
unmarshaler, the offending value; a refusal carries a byte offset and — for
the one thing a caller can act on — where it failed, as a JSON Pointer, through
`PointerOf`. The pointer is built from the document's member names and
indices: a location, never a value, still the document's text, bounded to
`MaxPointerBytes` (256) and cut at a separator so what remains names an
ancestor. A read that FAILED keeps the transport's message as a field, the way
`service/resilience` carries a cause, because it is not the document's.

### D4 — a request body is three more rules

`DecodeRequest(w, req, v, maxBytes)`:

- the body goes through `http.MaxBytesReader`, so a body past the bound is
  `DocumentTooLarge` AND net/http closes the connection instead of draining
  what the client keeps sending — read back as `Connection: close` in the
  suite;
- an empty body is `DocumentEmpty` before its media type is looked at, so a
  caller for whom a body is optional treats that ONE code as "no body", and one
  for whom it is required answers 400;
- a non-empty body must declare `application/json` or a structured-syntax
  `+json` type (RFC 6839 §3.1), or it is `MediaTypeUnsupported` (415) and is
  not parsed at all.

### D5 — placement: the codec's tree, its own facade, not a Format

The strictness is a property of reading JSON, so the package lives under
`internal/service/codec`; it registers no Format, because a registered codec
has no per-call bound and no media type, and `codec.Unmarshal("json")` must not
silently change meaning. It has its own facade, `pkg/v1/codec/strictjson`,
because `pkg/v1/codec` blank-imports all sixteen codecs and a server that reads
JSON bodies should not link BSON, CBOR, MessagePack, TOML and YAML to do it —
the reason `pkg/v1/hash` and `pkg/v1/sign` stand apart from `pkg/v1/crypto`.

## Consequences

- The framework's two copies — `decodeBody` and `studioBody` — become one call
  each, and `bodyProblem` goes: the SDK's refusal has the code, the status and
  the pointer. Its own error wire (`invalid_argument`, `too_large`,
  `unsupported_media_type`) is mapped from the SDK code, as it already maps the
  other SDK reasons.
- The two copies disagreed about an empty body with no Content-Type: the
  endpoint accepted it as no body, the console answered 415. The SDK answers
  `DocumentEmpty` for both, and the console's answer becomes a 400.
- Every refusal is a 400 except the three that are not: 413, 415, and 500 for
  a call no input can satisfy.

## Breaking changes

None. `strictjson` is a new package in this change set; the `json` codec is
unchanged.

## Alternatives considered

- **`encoding/json` with `DisallowUnknownFields` and a `More()` check.** Closes
  two rows of the table and leaves duplicates, case variants and invalid UTF-8
  accepted.
- **A `json-strict` Format in the registry.** No per-call bound, no request
  semantics, and a second name for "JSON" that dispatches differently from the
  first.
- **The pointer in Public.** It is the input's text; the fixed sentence plus an
  accessor lets the caller choose to show it, and render it as untrusted.
- **Keep the decoder's error as the cause.** `errors.Unwrap` would hand back
  the quoted value to whoever logs the chain.
- **Only the HTTP function.** The bound, the refusals and the value-free errors
  are the substance and none of them is HTTP's; a caller reading a document
  from a file or a queue wants the same decoder.

## Deferred

- **A strict decode for other formats.** Nothing asks for one yet, and each
  format's "one document, one reading" is its own list.
- **Violations over the decoded value.** Structure is this package's; values
  are `pkg/v1/validation`'s, which a handler runs after a successful decode.

## Verification

- `internal/service/codec/strictjson/strictjson_external_test.go` — every row of
  the table refused with its code, the bound exact at the boundary and against
  an endless reader (`TestDecodeReadsNoFurtherThanTheBound`), the misconfigured
  calls refused before a byte is read, the pointer at an array element, at an
  unknown member and bounded, the status of every sentinel, and
  `TestRefusalsNeverQuoteTheDocument`, which plants a value in each refusal's
  position and greps Error, Public, Private, every field and every unwrapped
  error.
- `internal/service/codec/strictjson/request_external_test.go` — media types,
  the empty body before the media type, and
  `TestAnOversizedBodyClosesTheConnection` over a real server.

## References

- [`encoding/json/v2`](https://pkg.go.dev/encoding/json/v2) — `RejectUnknownMembers`, and the defaults that refuse duplicates, case variants, invalid UTF-8 and trailing data.
- [`net/http.MaxBytesReader`](https://pkg.go.dev/net/http#MaxBytesReader) — the bound that also closes the connection.
- [RFC 6839 §3.1](https://www.rfc-editor.org/rfc/rfc6839#section-3.1) — the `+json` structured-syntax suffix.
- [RFC 6901](https://www.rfc-editor.org/rfc/rfc6901) — JSON Pointer.
