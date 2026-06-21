# ADR 0023 — Schema codecs (M6): opt-in, under third-party/codec

- **Status**: Accepted
- **Date**: 2026-06-21
- **Deciders**: SDK maintainers
- **Related**: ADR 0003 (codec package, M6 deferred set), ADR 0021 (BSON — document-bound, in-tree), ADR 0022 (HCL — first third-party codec), ADR 0005/0006 (error-code registry)

## Context

ADR 0003 §M6 deferred the **schema-bound** family: Protobuf, Avro, Cap'n Proto.
These differ fundamentally from the M1–M5 codecs (json/yaml/cbor/…/bson):

- The M1–M5 codecs are **structural** — they serialise an arbitrary Go value by
  reflection. The public registry encodes this as a hard contract:
  `pkg/v1/codec`'s `TestUniversalRoundtripAllCodecs` asserts that **every**
  registered Format round-trips a plain Go struct.
- Schema codecs are **schema-bound** — Protobuf encodes only `proto.Message`
  values (generated from `.proto`); Avro needs a schema; Cap'n Proto needs
  generated readers. None can round-trip an arbitrary struct, so none can
  satisfy the universal contract.

Putting a schema codec in the default registry (blank-imported by
`pkg/v1/codec`) would break that contract and mislead consumers into expecting
universal behaviour.

## Decision

1. **Schema codecs are OPT-IN and live under `third-party/codec/`** (the root
   module, alongside HCL — ADR 0022). They register the Format via the
   process-wide `core/codec` registry, but `pkg/v1/codec` does NOT blank-import
   them, so they are absent from the default registry, its Format constants, and
   `TestUniversalRoundtripAllCodecs`. A consumer activates one explicitly:
   `import _ "…/third-party/codec/protobuf"`.
   - This placement is also forced by Go's `internal/` rule: a consumer-opt-in
     codec must be importable by consumers, so it cannot live under
     `internal/service/codec`. `third-party/` is the only consumer-importable
     home (mirrors the writer quarantine, ADR 0012).
2. **They still implement the universal `core/codec.Codec` interface**, requiring
   the value to satisfy the schema type (`proto.Message`), exactly as BSON
   requires a document (ADR 0021) — a non-conforming value returns the codec's
   `*_MARSHAL_FAILED` / `*_UNMARSHAL_FAILED` sentinel. So `codec.Marshal("protobuf", msg)`
   works uniformly once activated.
3. **Protobuf is the first concrete schema codec** (`third-party/codec/protobuf`,
   `google.golang.org/protobuf`). Non-streaming + Appender; 10 MiB cap; error
   block `0.3.38.*`. Tested with `structpb.Struct` (a well-known `proto.Message`
   — no `.proto` codegen in the test).
4. **Avro and Cap'n Proto are deferred**, each its own follow-up: Avro needs a
   schema-registry story (the schema is not embedded the way a `proto.Message`
   carries its descriptor); Cap'n Proto needs generated code and a build-step
   integration. Both are larger than "add a codec" and get their own ADR when
   scheduled.

## Consequences

- New `third-party/codec/protobuf` package with the standard layout + an
  `audit_srcs` filegroup wired into `//:audit_sources`. The protobuf dep moves to
  the **root** `go.mod` (third-party home); `internal/service` is untouched.
- Public registry counts are **unchanged** (22 Formats / 14 in-tree codecs) —
  protobuf is opt-in, not part of the default `pkg/v1/codec` set.
- A documented pattern now exists for any future schema codec: opt-in, under
  `third-party/codec/`, schema-type-bound, not in the universal round-trip test.

## Why not

- **In-tree + blank-imported (the BSON precedent)** — rejected: BSON marshals any
  struct as a document, so it honours the universal contract; Protobuf cannot.
  A schema codec in the universal registry breaks `TestUniversalRoundtripAllCodecs`
  and the consumer expectation it encodes.
- **A skip-list in the universal round-trip test** — rejected: it would weaken
  the "no codec slips past CI" guarantee and paper over a real category
  difference. Opt-in placement expresses the difference structurally.
- **Implement Avro/Cap'n Proto now** — rejected: each needs schema/codegen
  machinery out of proportion to this ADR; deferred with the rationale above.

## References

- ADR 0003 §M6, ADR 0021 (BSON, document-bound in-tree), ADR 0022 (HCL,
  first third-party codec), ADR 0005/0006 (code registry).
