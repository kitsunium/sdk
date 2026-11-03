<!-- updated: 2026-11-03T22:00:00Z -->
# internal/service/data/codec/multipart/

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
validated by `validateBoundary` — the RFC 2046 rule `mime/multipart.Writer.SetBoundary`
applies — and used verbatim. A generated one is drawn as that writer draws it:
thirty `crypto/rand` bytes in hex.

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
| `MaxTotalBytes` | 64 MiB (`DefaultMaxTotalBytes`) | the WHOLE body — every byte read or written, delimiters and part headers included, not only the sum of part bodies (#908); without it the real ceiling would be 32 MiB × 1024 = 32 GiB, which is not a bound anybody chose |

**A zero field means "use the documented default", never "unlimited"** —
ADR 0031's clamp arm. **A negative field is refused** with `LIMITS_INVALID`
naming the knob (never echoing the value): the SDK can supply a ceiling the
caller forgot, but it cannot tell a typo from a request for no ceiling at all.
There is deliberately no spelling of "unlimited".

The refusal happens **at construction**, not at first use, because
`NewWithLimits` is a new API with no existing call sites — the `(value, error)`
shape ADR 0031 records as the preferred v2 form costs nothing here. The per-part
read uses the `internal/service/data/transform/bounded.go` template — an
`io.LimitReader` one byte past the bound, so an over-cap part is detected as
*overflow* rather than silently truncated — with two corrections:

- **The bound is the tighter of `MaxPartBytes` and what is left of
  `MaxTotalBytes`** (`counter.readBudget`). Reading to `MaxPartBytes` first and
  charging the total afterwards let the LAST part overrun the aggregate by up to
  a whole part — 1.5× with the defaults, unbounded when `MaxPartBytes` exceeds
  `MaxTotalBytes` — and the refusal now names the knob that actually stopped the
  read (the per-part cap on a tie, matching `admitPart`'s order).
- **Headers count** (#908). The three bounds used to be charged to part
  bodies after `NextPart` returned, so a part's header block — parsed by
  mime/multipart itself — was bounded only by the GODEBUG
  `multipartmaxheaders` defaults, which a process can lift at run time (the
  form codec's argument for its own pair cap). `decoderFor` now hands
  mime/multipart the body through a `wireBudget` (`counter.go`) that charges
  every byte it pulls, its read-ahead included, against `MaxTotalBytes` and
  never pulls more than one byte past it: a header block of twice the ceiling
  is refused with `LIMIT_EXCEEDED` / knob `MaxTotalBytes` before any part is
  handed back (`TestDecodeChargesHeadersAgainstTheTotal`), and once the
  ceiling is crossed no further part is framed. The encoder charges the bytes
  it writes the same way (`admitEncodedPart`, reserving the closing
  delimiter), so a body Marshal accepts is one the decoder reads back. The
  per-part body accounting above is unchanged; it is now always at most what
  the wire counter charges. **Semantic change:** a body whose part bodies sum
  to exactly `MaxTotalBytes` was admitted and is now refused — the bound is
  the body as transmitted (`TestDecodeChargesTheLastPartAgainstTheAggregate`
  admits a body exactly at its own length and refuses one byte less).
- **No probe byte at `math.MaxInt64`** (`probeSize`). `max+1` wraps negative
  there, `io.LimitReader` reads nothing through a negative limit, and with
  `MaxPartBytes` at `MaxInt64` — an accepted value — every part decoded
  silently empty. No `[]byte` can exceed that bound, so there is nothing to probe.

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
