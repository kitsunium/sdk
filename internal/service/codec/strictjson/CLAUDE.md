# internal/service/codec/strictjson/

## Purpose

Decodes exactly ONE JSON document into a Go value, within a byte bound, refusing
what a permissive decoder lets through, with errors that never quote the input
(ADR 0102). `Decode` for any reader; `DecodeRequest` for an HTTP request body;
`PointerOf` for where a refused document went wrong. Public facade:
`pkg/v1/codec/strictjson`.

Stdlib only — `encoding/json/v2` and `encoding/json/jsontext`, which Go 1.27
ships without an experiment flag. Not a codec: it registers no Format.

## Contents

| File | Role |
|---|---|
| `strictjson.go` | package doc, `Decode`, `checkArguments`, `decode`, `classify`, `located`, `boundPointer`, `PointerOf` |
| `reader.go` | `boundedReader` — hands the decoder the bound plus one byte, remembers what it delivered and the first real read failure, and `verdict` |
| `request.go` | `DecodeRequest` — `http.MaxBytesReader`, the empty-body peek, the media-type check (`application/json` or `+json`, RFC 6839) |
| `codes.go` / `errors.go` | the eight `0.3.72.*` codes and their sentinels, each with its HTTP status |

## Why-this-shape

- **Five ways one document is read two ways.** `encoding/json`'s defaults drop
  an unknown member, match a member to a field case-insensitively, let the last
  of two duplicate names win, ignore data after the value, and read any length.
  None of them fails, and each lets a document mean one thing to this program
  and another to whatever parsed it first. `encoding/json/v2`'s defaults refuse
  duplicates, case variants, invalid UTF-8 and trailing data; `RejectUnknownMembers`
  adds the fourth; the bound is this package's.
- **The bound is on READING, not a check afterwards.** `boundedReader` hands out
  the bound plus one byte and then reports the end of input, so a client that
  never stops sending costs `maxBytes + 1` bytes and the verdict is taken from
  the count. A document past the bound is refused whatever the decoder made of
  the part it read — that part is a truncation, not the document.
  `TestDecodeReadsNoFurtherThanTheBound` pins it against an endless reader.
- **The reader's verdict precedes the decoder's.** Too large, then a failed
  source, then an empty one — only then is the decoder's own error classified,
  because each of the three makes the decoder's conclusion an artefact.
- **No cause is kept from the decoder.** `jsonv2`'s errors quote the offending
  member name and, through a field's own unmarshaler, the offending value; this
  package's refusals are built with the sentinel as the origin and carry only a
  byte offset and a pointer. A failed READ keeps the transport's message as a
  field (the `service/resilience` pattern), since that is not the document's.
  `TestRefusalsNeverQuoteTheDocument` plants a value in each refusal's position
  and greps every rendering: `Error`, Public, Private, every field, and every
  error the chain unwraps to.
- **The pointer is the ONE place the document's text appears.** It is the only
  location a caller can act on, it is built from member names and indices —
  never a value — and it is bounded to 256 bytes, cut at a token separator so
  what remains still names an ancestor. The facade says to render it as
  untrusted.
- **A request body is three more rules, and only three.** `http.MaxBytesReader`
  so net/http closes the connection instead of draining an oversized body
  (`TestAnOversizedBodyClosesTheConnection` reads `Connection: close` back); an
  empty body is `DocumentEmpty` BEFORE its media type is looked at, so a caller
  with an optional body treats that one code as "no body"; and a non-empty body
  must declare JSON or it is not parsed at all.
- **Zero is refused** (ADR 0031). A bound of zero reads as "unlimited" or as
  "nothing", opposites, and the first is a memory-exhaustion bug. A target that
  is not a non-nil pointer is refused before a byte is read.

## Do NOT

- Keep a `jsonv2` error in the chain, or format one into Private. Its text is
  the input's.
- Make the bound optional, or default it. The caller knows what a body may be.
- Register this as a codec Format. A registered codec has no per-call bound and
  no media-type check, and `codec.Unmarshal("json")` would silently stop
  meaning what it has always meant.
- Accept `text/json` or a missing Content-Type. A body that does not say it is
  JSON is not parsed as JSON.

## Verification

```sh
bazel test --config=race //internal/service/codec/strictjson:strictjson_test
cd internal/service && GOWORK=off go test -race ./codec/strictjson/
```
