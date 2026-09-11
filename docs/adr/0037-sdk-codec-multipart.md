# ADR 0037 — multipart/form-data: the boundary is not in the body, so the codec grows an extension interface

- **Status**: Accepted
- **Date**: 2026-09-09
- **Deciders**: SDK maintainers
- **Related**: [ADR 0003](0003-sdk-codec-package.md) (universal codec contract), [ADR 0036](0036-sdk-codec-form-urlencoded.md) (the other web form encoding), [ADR 0031](0031-policy-zero-values-are-never-inert.md), [ADR 0035](0035-pp-range-ownership-enforcement.md) (the range this claims)

## Context

`mime/multipart` is stdlib, so the encoding costs nothing in dependencies. The
difficulty is structural, and it is worth recording because it is the first time
the codec contract has failed to express a format outright.

**The delimiter lives in the `Content-Type` header, not in the body.** The
`Codec` contract is `Marshal(v any) ([]byte, error)` / `Unmarshal(data []byte,
v any) error`. It carries bytes and a value. It does not carry a header. So the
one parameter this format needs most has nowhere to travel.

Three sub-problems hide behind that one sentence, and they do not have the same
answer. Forcing a single abstraction over them would have produced a codec that
is subtly wrong in one of the three.

## Decision

**Three sub-problems, three answers.**

1. **Encoding — re-emission plus a recovery helper.** The delimiter appears on
   every boundary line, so the body is self-describing; what `Marshal` cannot
   return is the *header*. `ContentType(data)` reads the delimiter back off the
   bytes `Marshal` just produced and formats the header via
   `mime.FormatMediaType`. A caller who needs a chosen boundary sets
   `FormValue.Boundary`, validated by `multipart.Writer.SetBoundary`.
   Rejected alternatives: mutating the caller's value (breaks
   concurrency-safety) and an out-of-band side channel (invisible at the call
   site).

2. **Decoding from `[]byte` — solved for every real body, not in general.**
   `Boundary(data)` scans a bounded window for the first `--`-prefixed line,
   then **confirms** the candidate against the closing delimiter `--<b>--`
   rather than trusting the first match. Exact for self-produced bytes and for
   every RFC 7578 producer, which emit no preamble.

3. **Streaming — an extension interface.** `NewDecoder(r)` sniffs via
   `bufio.Reader.Peek` (non-consuming), but that is not enough for the two real
   HTTP cases: a server already **has** the authoritative boundary from the
   header, and a client must **set** the header before the first byte. Neither
   fits the domain's fixed signatures. Hence `BoundaryCodec` (boundary-explicit
   constructors, error returned) and `BoundaryProvider` (`Encoder.Boundary()`),
   reached by type assertion exactly as `Appender` already is.

**What the package refuses to cover is stated, not hidden:**

- **A body with a `--`-prefixed preamble.** RFC 2046 permits an arbitrary
  preamble; such a line is indistinguishable from the delimiter.
  `Unmarshal(data, v)` cannot be made correct for it, so the docs name
  `NewDecoderWithBoundary` as the authoritative path instead of pretending the
  sniff is an oracle.
- **Byte-streaming inside one part.** `Decode(v any)` has nowhere to hand back
  an `io.Reader` the caller must drain before the next call. Streaming is
  *between* parts; a part body is materialised under `MaxPartBytes`.
- **`Extensions()` returns nothing** — no registered suffix exists, and
  inventing `.multipart` would make `FromExtension` resolve a name no producer
  emits.
- `multipart/mixed` / `related` / `byteranges`, `Content-Transfer-Encoding`, and
  preamble/epilogue preservation, each with its reason.

**Arbitrary values go through a JSON-mediated single part named `_json`** (the
`baseenc` precedent), so the universal round-trip contract holds *inside* the
codec rather than through a sixth entry in the facade's constrained-codec table.

**ADR 0031 applies to the size caps**: a bound of 0 does not mean "unlimited".

## Consequences

- **The codec domain now has extension interfaces.** `BoundaryCodec` and
  `BoundaryProvider` join `Appender` and `StreamingCodec` as capabilities
  discovered by type assertion. This is the pattern to follow when a future
  format needs a parameter the fixed signatures cannot carry — and the reason
  this ADR exists rather than a package comment.
- Registry counts reach 24 Formats over 16 in-tree codec packages.
- Value types carry role suffixes (`FormValue`, `PartValue`, `LimitsConfig`)
  because `KTN-STRUCT-ROLE` rejects the bare nouns.

## Why not

- **Widen the `Codec` interface to carry a `Content-Type`.** Rejected: it would
  change every codec's signature to serve one format, and the parameter is
  meaningless for the other twenty-three.
- **Put the boundary in a package-level variable or a context.** Rejected:
  invisible at the call site and hostile to concurrency — two encoders in one
  process would fight.
- **Refuse `Unmarshal([]byte)` entirely and expose only the streaming path.**
  Rejected: it works for every producer that exists in practice, and refusing it
  would make the format the only registry member without the universal verb.

## References

- `internal/service/codec/multipart/CLAUDE.md` §The boundary problem, §Not covered
- RFC 7578 (multipart/form-data), RFC 2046 §5.1 (the preamble)
- ADR 0003 (codec contract), ADR 0035 (range `0.3.41` ownership)
