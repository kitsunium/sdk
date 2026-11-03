<!-- updated: 2026-11-03T00:00:00Z -->
# pkg/v1/data/codec/jcs/

## Purpose

Public facade for RFC 8785 canonical JSON (#895): `Canonicalize` and `Marshal`
forward to `internal/service/data/codec/jcs`; `CodeNotJSON`, `CodeNumberInvalid`,
`CodeMarshalFailed` and their sentinels alias `internal/core/data/codec/jcs`
(ADR 0160). No logic lives here.

## Why a package of its own, under codec

A canonical form is a policy over JSON, not a Format — `strictjson`'s reason
(ADR 0102) — and its own facade keeps `pkg/v1/data/codec`'s sixteen codecs out
of a program that only signs documents.

## Do NOT

- Add logic here; it belongs in `internal/service/data/codec/jcs`.
