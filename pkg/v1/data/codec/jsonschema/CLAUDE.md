<!-- updated: 2026-11-03T00:00:00Z -->
# pkg/v1/data/codec/jsonschema/

## Purpose

Public facade for the JSON Schema 2020-12 validator (#882, ADR 0210):
`NewValidator` forwards to `internal/service/data/codec/jsonschema`, `Config`
and `Validator` alias its types, and the codes and sentinels alias
`internal/core/data/codec/jsonschema` (ADR 0160). No logic lives here.

## Why a package of its own, under codec

A validator is a policy over JSON documents, not a Format — strictjson's
reason (ADR 0102) — and its own facade keeps the sixteen codecs out of a
server that only validates request bodies.

## Do NOT

- Add logic here; it belongs in `internal/service/data/codec/jsonschema`.
