<!-- updated: 2026-09-11T00:00:00Z -->
# internal/service/codec/multipart/

## Purpose

RFC 7578 `multipart/form-data` — the shape every file upload and HTML form
post arrives in. `mime/multipart` is stdlib, so this codec adds **zero
dependencies**.

Unlike every other codec in this tree, the format is a **container**, not a
value serialisation: it frames a list of named sections, each with its own
headers. Its native Go shape is therefore `FormValue` (a delimiter plus
`[]PartValue`), and it is the first codec here to implement
`core/codec.StreamingCodec` because the format exists to *not* be loaded whole.

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
`codec.Marshal(codec.Multipart, anyValue)` hold without adding a sixth entry to
`pkg/v1/codec/promote.go`'s constrained-codec table.

## The boundary problem

**This is the real design tension in this package, and it is not fully
resolvable inside the current `Codec` contract. Read this before changing
anything here.**

The RFC 2046 delimiter that separates the parts is announced in the message's
`Content-Type` header (`multipart/form-data; boundary=…`), **not in the body**.
The domain contract is `Marshal(v any) ([]byte, error)` /
`Unmarshal(data []byte, v any) error` — there is nowhere to put a header. Three
sub-problems, three different answers:

**1. Encoding — solved, by re-emission plus a recovery helper.** The boundary
is written on every delimiter line, so a well-formed body is *self-describing*.
What `Marshal` cannot hand back is the header the caller must send alongside
the bytes. Rather than mutate the caller's value (which would break "safe for
concurrent use") or invent an out-of-band channel, the package exposes
`ContentType(data)`, which reads the delimiter back off the bytes `Marshal`
just returned and formats the full header value through `mime.FormatMediaType`.
A caller who needs a *chosen* boundary sets `FormValue.Boundary`; it is
validated by `mime/multipart.Writer.SetBoundary` and used verbatim.

**2. Decoding from `[]byte` — solved for every real body, NOT solved in
general.** `Unmarshal` recovers the delimiter with `Boundary(data)`: it scans a
bounded window for the first `--`-prefixed line and takes what follows as the
candidate. Only a line that itself ends in `--` is ambiguous — a zero-part
body is nothing but its close delimiter, while a boundary may also end in
`--` — and only then is the body searched for `--<boundary>--` to decide
between the two readings. Any other valid candidate is the answer whether its
close delimiter is found or not, so the body is not searched at all: the
search used to run on every body, before any size limit, and cost 132 ms per
63 MiB of hyphens against 3 µs now.
This is exact for every body this package produced and for every RFC 7578
producer in the wild, because form-data producers emit no preamble.

It is **not an oracle**. RFC 2046 permits an arbitrary preamble before the
first delimiter; a preamble line that both starts with `--` and parses as a
valid boundary would be mistaken for the delimiter. That case is **refused
rather than faked**: it is named here, and the authoritative path is
`BoundaryCodec.NewDecoderWithBoundary(r, boundary)`, where the caller supplies
the boundary it read from the real header. There is no way to make
`Unmarshal(data, v)` correct for that input, and pretending otherwise would be
worse than saying so.

**3. Streaming — solved by an extension interface.** `NewDecoder(r io.Reader)`
sniffs the head of the stream with `bufio.Reader.Peek`, which does **not**
consume it, so `mime/multipart.Reader` still sees a complete body. The sniff
reads no further than the answer needs: it returns once the first delimiter
line is complete, rather than waiting for the whole 4 KiB window — which used to
stall `NewDecoder` on a live producer that sent a short prefix and paused. Only
a first line ending in `--` still waits for the window or the end of the
stream, since only the bytes after it can tell a zero-part body from a
boundary that itself ends in `--`. But an HTTP
server already *has* the authoritative boundary, and an HTTP client must set
the header *before* it writes the first body byte — neither fits
`NewEncoder(w) Encoder` / `NewDecoder(r) Decoder`, whose signatures the domain
fixes. Hence `BoundaryCodec` (both constructors, boundary explicit, error
returned) and `BoundaryProvider` (`Encoder.Boundary()` so a streaming client
can set its header first). Both are reached by type assertion, exactly like
`codec.Appender`.

`codec.Decoder` has no error channel on its constructor, so a `NewDecoder`
whose sniff fails returns a decoder that reports the typed `BOUNDARY_INVALID`
from its first `Decode` — and answers `More()` **true exactly once**, so the
idiomatic `for dec.More() { dec.Decode(&v) }` loop cannot swallow it.

## Memory bounds (ADR 0031)

`LimitsConfig` carries three ceilings, enforced on **both** directions —
the encoder refuses what the decoder would refuse, so the codec never emits a
body it cannot read back:

| Knob | Default | Guards against |
|---|---|---|
| `MaxPartBytes`  | 32 MiB (`DefaultMaxPartBytes`)  | one crafted part driving `io.ReadAll` to an OOM |
| `MaxParts`      | 1024 (`DefaultMaxParts`)        | a boundary flood — cheap for the attacker, a header parse each for us; the byte bounds do not constrain a *count* on a streaming reader |
| `MaxTotalBytes` | 64 MiB (`DefaultMaxTotalBytes`) | the aggregate; without it the real ceiling would be 32 MiB × 1024 = 32 GiB, which is not a bound anybody chose |

**A zero field means "use the documented default", never "unlimited"** —
ADR 0031's clamp arm. **A negative field is refused** with `LIMITS_INVALID`
naming the knob (never echoing the value): the SDK can supply a ceiling the
caller forgot, but it cannot tell a typo from a request for no ceiling at all.
There is deliberately no spelling of "unlimited".

The refusal happens **at construction**, not at first use, because
`NewWithLimits` is a new API with no existing call sites — the `(value, error)`
shape ADR 0031 records as the preferred v2 form costs nothing here. The per-part
read uses the `internal/service/transform/bounded.go` template — an
`io.LimitReader` one byte past the bound, so an over-cap part is detected as
*overflow* rather than silently truncated — with two corrections:

- **The bound is the tighter of `MaxPartBytes` and what is left of
  `MaxTotalBytes`** (`counter.readBudget`). Reading to `MaxPartBytes` first and
  charging the total afterwards let the LAST part overrun the aggregate by up to
  a whole part — 1.5× with the defaults, unbounded when `MaxPartBytes` exceeds
  `MaxTotalBytes` — and the refusal now names the knob that actually stopped the
  read (the per-part cap on a tie, matching `admitPart`'s order).
- **No probe byte at `math.MaxInt64`** (`probeSize`). `max+1` wraps negative
  there, `io.LimitReader` reads nothing through a negative limit, and with
  `MaxPartBytes` at `MaxInt64` — an accepted value — every part decoded
  silently empty. No `[]byte` can exceed that bound, so there is nothing to probe.

## Header values are refused, never repaired

`Name`, `FileName` and `ContentType` are written into the part's header block,
and `mime/multipart.Writer.CreatePart` writes header values **verbatim**: a CR
or LF inside one ends the line, and the rest becomes header lines — or, after an
empty line, body — the caller never wrote. The encoder refuses a CR, LF or NUL
in any of the three with `VALUE_INVALID` **before** `CreatePart` writes a byte,
naming the field (`field` = `Name` / `FileName` / `ContentType`) and never the
value — ADR 0064's stance on mail headers. It deliberately does not do what
`mime/multipart`'s own escaper does (percent-encode CR and LF): that is a
repair, delivering a filename the caller never supplied while reporting
success. Quotes and backslashes are still escaped inside the quoted parameters
(`quoteEscaper`), and UTF-8 is written raw (RFC 7578 §4.2). Pinned by
`TestEncoderRefusesHeaderInjection` and `TestEncodedHeadersReparseWithTheStdlib`,
which requires the stdlib reader to read back exactly the header block asked for.

## Error codes (range `0.3.41.*`)

| Code | Var | Trigger |
|---|---|---|
| `0.3.41.1` | `MarshalFailed`    | `mime/multipart` failed writing a part header/body/closing delimiter, or `encoding/json` rejected the value on the JSON-mediated path |
| `0.3.41.2` | `UnmarshalFailed`  | malformed part header, truncated body, a part with no form-data name (RFC 7578 §4.2 — the encoder refuses to write one, so it is refused on the way in too), non-JSON `_json` part, or a body with no `_json` part when the target is not a `*FormValue` |
| `0.3.41.3` | `ValueInvalid`     | `PartValue` with no `Name`; a CR, LF or NUL in `Name` / `FileName` / `ContentType` (field `field` names which, never the value); nil `*FormValue` / `*PartValue`; decode target that is not a non-nil pointer |
| `0.3.41.4` | `BoundaryInvalid`  | delimiter not recoverable from the body, or a caller-supplied boundary outside RFC 2046 (1–70 bchars, no trailing space) |
| `0.3.41.5` | `LimitExceeded`    | `MaxPartBytes` / `MaxParts` / `MaxTotalBytes` crossed — fields carry `knob`, `bound`, `got` |
| `0.3.41.6` | `LimitsInvalid`    | a negative `LimitsConfig` field (ADR 0031 refusal) — field carries `knob` |

The range is allocated to this package in `codeRangeOwners`
(`internal/kernel/errs/registry_ownership_external_test.go`, ADR 0035).

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
  cross-codec composition belongs in `pkg/v1/codec` or in the caller.
- Add a file extension to make `FromExtension` "work". See §Extensions.
- Percent-encode or strip a CR / LF / NUL in a part header value to make it
  "fit". It is refused. See §Header values.
- Let a zero `LimitsConfig` field mean "unlimited". See §Memory bounds.
- Assume `Boundary(data)` is authoritative. It is a recovery. See §2 above.
- Trust `More()` alone to mean "no error happened" — it answers true once for a
  parked failure precisely so `Decode` gets to report it.

## Verification

```
bazel test --config=race //internal/service/codec/multipart:multipart_test
```

Locally, without Bazel:

```
cd internal/service && GOWORK=off go test -race ./codec/multipart/
```
