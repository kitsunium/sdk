<!-- updated: 2026-10-03T00:00:00Z -->
# internal/service/data/codec/strictjson/httpbody/

## Purpose

`DecodeRequest`: the JSON body of an HTTP request decoded by the strict decoder
of the parent package (ADR 0102), plus the three things only an HTTP body has.
It was `strictjson/request.go` until the decoder alone had to link no
`net/http`; it is a package of its own for exactly that reason. Public facade:
`pkg/v1/data/codec/strictjson/httpbody`.

## Contents

| File | Role |
|---|---|
| `httpbody.go` | `DecodeRequest`; `emptyOrUnreadable` (the first read's verdict), `isJSONMediaType` (`application/json` or any `+json` type, RFC 6839 §3.1), `isMaxBytesError` (net/http's refusal of a body past the bound) |

## Why-this-shape

- **Three rules, and only three.** `http.MaxBytesReader`, so net/http closes
  the connection instead of draining an oversized body
  (`TestAnOversizedBodyClosesTheConnection` reads `Connection: close` back); an
  empty body is `DocumentEmpty` BEFORE its media type is looked at, so a caller
  with an optional body treats that one code as "no body"; a non-empty body
  must declare JSON or it is `MediaTypeUnsupported` (415) and is not parsed.
- **Everything else is the parent's, through four hooks.** The arguments are
  refused by `strictjson.CheckArguments` before the request is touched, a nil
  request by `strictjson.Misconfigured("req")`, a failed first read by
  `strictjson.Unreadable`, and the document by `strictjson.DecodeChecked` with
  `isMaxBytesError` as its size recogniser — so a body past the bound is
  `DocumentTooLarge` whichever reader saw it first. The refusals are the core's
  sentinels (`internal/core/data/codec/strictjson`); this package declares no
  code.

## Do NOT

- Accept `text/json` or a missing Content-Type. A body that does not say it is
  JSON is not parsed as JSON.
- Re-implement the bound, the verdict order or the classification here: they
  are the parent's, and a body must be refused exactly as a document is.
- Move this back into `strictjson`. Its `net/http` import is the one thing the
  decoder must not link.

## Verification

```sh
bazel test --config=race //internal/service/data/codec/strictjson/httpbody:httpbody_test
cd internal/service && GOWORK=off go test -race ./data/codec/strictjson/httpbody/
```
