# ADR 0006 — Error Code Registry Extension (logger v2 + ring)

**Status**: Accepted
**Date**: 2026-04-23
**Deciders**: @kodflow
**Amends**: ADR 0005 §Registry (dotted-quad error codes)
**Related**: ADR 0002 §Registry (superseded), post-audit plan `dreamy-nibbling-bear.md`

## Context

PR #12 (`feat(logger): v2 zero-alloc multi-sink architecture`) introduced
10 new packages that emit typed SDK errors: `internal/kernel/ring`, the
logger v2 sink transports (`sink/console`, `sink/file`, `sink/syslog`),
and the logger v2 middleware combinators (`middleware/multi`, `async`,
`route`, `failover`, `sample`, `recover`). Each package needs a dotted-quad
range from ADR 0005's allocation table.

During PR #12's rebase onto post-#13 main, ranges were assigned ad-hoc by
the reviewer without an ADR amendment — `0.1.3.*` for ring and `0.3.13.*`
through `0.3.21.*` for the logger sub-packages. ADR 0005's registry is
the authoritative source; this ADR formalises those assignments and
resolves the documentary drift flagged by the post-#12 audit (findings
#8, #33, #34).

## Decision

### Extended registry (appended to ADR 0005 §Registry)

| Package | Dotted range | Codes in this ADR |
|---|---|---|
| `internal/kernel/ring` | `0.1.3.*` | `CodeRingFull=0.1.3.1`, `CodeRingEmpty=0.1.3.2`, `CodeRingCapZero=0.1.3.3` |
| `internal/service/logger` (extension) | `0.3.1.*` | `CodeEncoderNil=0.3.1.3`, `CodeSinkRequired=0.3.1.4` — extends the 4 codes already registered in ADR 0005 |
| `internal/service/codec/ndjson` (extension) | `0.3.11.*` | already in ADR 0005; format only typed via Wave 4 — no new codes |
| `internal/service/codec/*` (Wave 4 typing) | `0.3.2.*` to `0.3.10.*` | all constants retyped as `errs.Code` — no new codes |
| **— range `0.3.12.*` RESERVED** | `0.3.12.*` | intentionally unassigned; next logger sub-package takes this slot |
| `internal/service/logger/sink/console` | `0.3.13.*` | `CodeWriterNil=0.3.13.1`, `CodeCtxCancelled=0.3.13.10`, `CodeWriteFailed=0.3.13.20` |
| `internal/service/logger/sink/file` | `0.3.14.*` | `CodePathEmpty=0.3.14.1`, `CodeOpenFailed=0.3.14.2`, `CodeCtxCancelled=0.3.14.10`, `CodeWriteFailed=0.3.14.20`, `CodeSyncFailed=0.3.14.30`, `CodeCloseFailed=0.3.14.40` |
| `internal/service/logger/sink/syslog` | `0.3.15.*` | `CodeSyslogAddrEmpty=0.3.15.1`, `CodeSyslogDialFailed=0.3.15.2`, `CodeSyslogWriteFailed=0.3.15.3`, `CodeSyslogCloseFailed=0.3.15.4`, `CodeSyslogProtoInvalid=0.3.15.5`, `CodeSyslogCtxCancelled=0.3.15.6` (added Wave 1 #7) |
| `internal/service/logger/middleware/multi` | `0.3.16.*` | `CodeFanoutWriteFailed=0.3.16.1` |
| `internal/service/logger/middleware/async` | `0.3.17.*` | `CodeAsyncStopped=0.3.17.1`, `CodeAsyncBufferFull=0.3.17.2`, `CodeAsyncCtxCancelled=0.3.17.3` (added Wave 1 #7) |
| `internal/service/logger/middleware/route` | `0.3.18.*` | `CodeRouteNoMatch=0.3.18.1` |
| `internal/service/logger/middleware/failover` | `0.3.19.*` | `CodeFailoverExhausted=0.3.19.1`, `CodeFailoverEmpty=0.3.19.2` |
| `internal/service/logger/middleware/sample` | `0.3.20.*` | `CodeSampleRateInvalid=0.3.20.1`, `CodeSampleDownstreamNil=0.3.20.2` |
| `internal/service/logger/middleware/recover` | `0.3.21.*` | `CodeRecoverPanicked=0.3.21.1`, `CodeRecoverDownstreamNil=0.3.21.2` |
| `pkg/v1/logger` (extension) | `1.1.0.*` | `CodeWriterRequired=1.1.0.1` (ADR 0005), `CodeSinkConfigRequired=1.1.0.2` (added PR #12) |
| `internal/service/codec/tlv` | `0.3.22.*` | `CodeTLVMarshalFailed=0.3.22.1`, `CodeTLVUnmarshalFailed=0.3.22.2`, `CodeTLVUnsupportedType=0.3.22.3`, `CodeTLVDepthExceeded=0.3.22.4`, `CodeTLVSizeExceeded=0.3.22.5`, `CodeTLVTruncated=0.3.22.6` |
| `internal/service/codec/flatbuffers` | `0.3.23.*` | `CodeFlatbuffersUnsupportedType=0.3.23.1`, `CodeFlatbuffersUnsupportedTarget=0.3.23.2`, `CodeFlatbuffersTruncated=0.3.23.3` |
| `internal/service/codec/baseenc` | `0.3.24.*` | `CodeBaseEncMarshalFailed=0.3.24.1`, `CodeBaseEncUnmarshalFailed=0.3.24.2`, `CodeBaseEncDecodeFailed=0.3.24.3`, `CodeBaseEncSizeExceeded=0.3.24.4` |

Total new assignments: **10 packages, 26 codes**. The 3-digit registry
audit (`internal/kernel/errs/registry_external_test.go`) verifies
uniqueness across all assigned codes.

### Serial numbering convention

Within a package's 256-slot block, serial numbers follow the original
ADR 0002 scheme mechanically:

- `.1` through `.9` — configuration / construction errors (nil input,
  empty argument, invalid enum).
- `.10` — context cancellation during a runtime call (Write / Flush).
- `.20` — I/O write failure (wrapping underlying cause, typically gets
  `errs.WithExitCode(74)` EX_IOERR).
- `.30` — flush/sync failure.
- `.40` — close failure.

Packages with fewer than 5 codes omit the sparse slots. The scheme is
documentary only — the audit enforces uniqueness, not a particular
layout within the block.

### Layer byte semantics (clarification — finding #34)

Layer 3 (the second hex octet) encodes "SDK service layer" for BOTH the
codec sub-tree AND the logger sub-tree. This means:

- `errors.Is(err, errs.NewPrefixMatcher(0x00_03_00_00, errs.MaskByLayer))`
  matches ANY service-layer error — codec JSON marshal failures **and**
  logger ring saturation alike.
- Consumers who need "codec errors only" or "logger errors only" must
  use `MaskByPackage` and name the specific package ranges.

Splitting codec into Layer=3 and logger into Layer=4 was considered and
rejected: it doubles the per-domain range cost for a zero-value routing
benefit that every consumer can already achieve via `MaskByPackage`.
Callers implementing dashboards or SLO rules should use the specific
package prefix, not the layer mask.

### `0.3.12.*` reserved slot

PP slot 12 was skipped when allocating the logger sub-packages (jumping
from codec/ndjson's `0.3.11.*` to sink/console's `0.3.13.*`). The slot
is intentionally reserved for the next logger-adjacent package that
needs codes — the first candidate is a `sink/http` transport tracked
for v1.1. Documenting the gap here prevents a future contributor from
assigning it by analogy without checking.

## Consequences

- **Documentary consistency**: the ADR 0005 registry is now synchronised
  with the code; the post-audit finding #8 (registry drift) is closed.
- **ADR 0002 legacy ranges**: the flat-int ranges 5100-5399 that ADR
  0002 assigned to `sample`, `recover`, `syslog` before ADR 0005 was
  accepted are now canonically `0.3.15.*`, `0.3.20.*`, `0.3.21.*`. A
  NOTE banner in ADR 0002's registry table flags those rows as legacy
  shorthand.
- **Future allocations**: new logger sub-packages get PP slot 22+
  (next unused). Codec sub-packages get PP slot 12 (next reserved) or
  continue from ndjson's 11+. The ADR 0005 registry table is the
  authoritative list — this extension table becomes part of it after
  merge.

- **Amended by ADR 0012** (writer subsystem v2) for the logger sub-tree
  — see ADR 0012 §11 for the new block reservations 0.3.25-79 plus the
  Layer-1 1.1.1.* facade range. The "PP slot 22+" guidance above stays
  valid for non-logger sub-trees.

## Deferred (not addressed here)

- **`KTN-ERRS-DOTTEDQUAD` linter rule** (ADR 0005 §Deferred): an
  AST-level rule that rejects `Define(<int-literal>, …)` so future
  additions cannot regress to the flat-int pattern. Tracked for a
  dedicated ktn-linter PR.
- **YAML-based canonical registry** (ADR 0005 §Deferred): tooling to
  codegen Code constants from a canonical `docs/adr/0005-error-codes.yaml`
  file. Tracked for the `tools/errs-codegen` PR.
- **Pre-commit audit**: a Makefile target that runs the registry
  uniqueness audit as part of `make sdk-lint`. Bazel test already covers
  this in CI; the local convenience target is a follow-up.

## References

- ADR 0005 — dotted-quad error codes — `docs/adr/0005-sdk-error-codes-dotted-quad.md`
- ADR 0002 — SDK errors package — `docs/adr/0002-sdk-errors-package.md`
- Registry audit — `internal/kernel/errs/registry_external_test.go`
- Post-#12 audit findings #8, #33, #34 — plan `dreamy-nibbling-bear.md`
