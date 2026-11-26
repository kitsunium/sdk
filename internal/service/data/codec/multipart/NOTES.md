# internal/service/data/codec/multipart — notes

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles.

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
