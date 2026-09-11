# ADR 0022 — HCL codec, quarantined under third-party/ (M5)

- **Status**: Accepted (decision stands; §Context.1 + §Why not mechanism corrected by [ADR 0034](0034-hcl-quarantine-rationale-corrected.md) — HCL *introduces* the banned `x/sys`, it does not downgrade it)
- **Date**: 2026-06-21
- **Deciders**: SDK maintainers
- **Related**: ADR 0003 (codec package, M5), ADR 0012 (third-party quarantine for vendor-heavy integrations), ADR 0021 (BSON codec — the library-backed-in-service precedent this ADR deliberately does NOT follow), ADR 0005/0006 (error-code registry)

## Context

ADR 0003 §M5 listed HCL among the deferred formats. HCL (HashiCorp
Configuration Language) is widely used for config (Terraform, Nomad, etc.).
Unlike BSON (ADR 0021), HCL has two properties that make it a poor fit for the
dep-light `internal/service/codec` tree:

1. **Heavy dependency graph.** `github.com/hashicorp/hcl/v2` pulls
   `github.com/zclconf/go-cty` and friends. Adding it to
   `internal/service/go.mod` **downgrades shared dependencies** — observed:
   `golang.org/x/sys` `v0.33`→`v0.20`, plus `x/text`/`x/sync`/`go-cty`. The
   `internal/service/proc` syscall code depends on a current `x/sys`; an HCL
   dep that drags it backwards across the whole service module is unacceptable.
2. **Decode-oriented, asymmetric.** HCL's Go ecosystem decodes (`gohcl.DecodeBody`
   / `hclsimple.Decode`) far more naturally than it encodes; value-level marshal
   exists only via `gohcl.EncodeIntoBody`, which is **struct-only** (top-level
   must be a struct with `hcl:"…"` tags).

## Decision

1. **Quarantine HCL under `third-party/codec/hcl`** (the ROOT module), NOT
   `internal/service/codec`. The root module already hosts vendor-heavy
   integrations (AWS/DB writers, ADR 0012); adding `hcl/v2` + `go-cty` there
   leaves `internal/service` (and `proc`) untouched — verified: the root
   module's `x/sys` stays at `v0.45.0` after the add.
2. **Opt-in, like the third-party writers.** A consumer blank-imports
   `…/third-party/codec/hcl` to register the `"hcl"` Format. **`pkg/v1/codec`
   does NOT import it** — the public module stays dep-light, so HCL is absent
   from `pkg/v1/codec`'s Format constants, registry count, and bijection tests.
3. **First third-party codec.** This establishes the `third-party/codec/`
   subtree mirroring `third-party/{aws,db}/writer/`; registration is via the
   process-wide `core/codec` registry exactly as in-tree codecs.
4. **Capabilities**: `Appender` yes; `StreamingCodec` no. Top-level marshal is
   struct-only — a non-struct returns `HCL_MARSHAL_FAILED` (reflection check
   before the library call), and a `gohcl.EncodeIntoBody` panic on an
   unsupported field type is **recovered** into the same sentinel.
5. **Error block `0.3.37.*`** (next free `PP` after BSON's `0.3.36`; verified
   against `//:audit_sources`). 10 MiB `maxHCLBytes` Unmarshal cap (CWE-400).

## Consequences

- New subtree `third-party/codec/hcl` with the standard codec file layout +
  an `audit_srcs` filegroup wired into `//:audit_sources` (so the AST audit
  catches code collisions). Root `go.mod`/`go.sum` + `MODULE.bazel` `go_deps`
  grow by `hcl/v2` + `go-cty`; `internal/service` is unaffected.
- `docs/error-codes.yaml` regenerated (`0.3.37.*` entries). The public
  registry count stays **22 Formats / 14 in-tree codecs** — HCL is opt-in and
  not part of the default `pkg/v1/codec` set, so those counts do not change.
- Consumers who want HCL accept the heavier graph by explicitly importing the
  third-party package — the cost is opt-in, not imposed on every SDK user.

## Why not

- **`internal/service/codec/hcl` (the BSON/ADR-0021 precedent)** — rejected: the
  `go-cty` dep downgrades `x/sys` across the whole service module, regressing
  `proc`. The library-backed-in-service precedent only holds for LIGHT codec
  libs (cbor/msgpack/toml/yaml/bson); HCL is not light.
- **Skip HCL entirely** — rejected: it is a listed M5 format and is genuinely
  useful for config payloads; quarantine delivers it without the dep cost.
- **A bespoke HCL encoder to avoid `go-cty`** — rejected: re-implementing HCL
  marshalling is out of proportion to the value; `gohcl.EncodeIntoBody` is the
  upstream-supported path.

## References

- ADR 0003 §M5, ADR 0012 (quarantine policy), ADR 0021 (BSON — contrasting
  in-service placement), ADR 0005/0006 (code registry).
