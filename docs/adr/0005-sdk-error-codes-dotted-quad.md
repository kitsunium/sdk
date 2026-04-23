# ADR 0005 — Dotted-Quad Error Codes (supersedes ADR 0002 §Registry)

**Status**: Accepted
**Date**: 2026-04-23
**Deciders**: @kodflow
**Supersedes**: ADR 0002 (Registry table only; all other sections of 0002 remain authoritative)
**Related**: ADR 0001 (multi-module layout)

## Context

ADR 0002 introduced layered numeric error codes (1xxx kernel / 2xxx core /
3xxx service / 4xxx public facade) packed as `int`. The flat layout served
early M1/M2 needs but two pressures have surfaced as the SDK grew:

1. **Compartmentalisation** — codes like `3211` (service/codec/json marshal)
   share prefixes with codes in other packages just because they happen to
   fall in the same thousand-block. Routing a dashboard rule or an HTTP
   status mapping to "all service/codec errors" required enumerating
   sub-ranges by hand.
2. **Major-version breakability** — the ADR 0002 table reserves `>= 10000`
   for `pkg/v2+`, but expresses no mechanism for a v2 to redraw its
   sub-ranges without colliding with v1 history.

The user's ask: **"can we think about this like IPv4 ranges?"** (CIDR /
prefix / mask subnetting). That decomposition naturally gives a built-in
major-version axis, hierarchical subnetting per layer and package, and
bit-level matching for runtime policy.

## Decision

Replace the flat `int` Code with a **32-bit dotted-quad `Code`**, exposed
as a named Go type `Code uint32` with layout `MM.LL.PP.SS`:

| Byte | Octet | Domain |
|---|---|---|
| 3 (top) | `MM` Major | SemVer major (0 = internal/unreleased, 1 = v1, 2 = v2, ...) |
| 2 | `LL` Layer | 0 = meta (kernel/errs only), 1 = kernel, 2 = core, 3 = service, (others reserved) |
| 1 | `PP` Package | Sub-slot within Layer (per-major, per-layer) |
| 0 (low) | `SS` Serial | Code within the Package |

### API surface

- `type Code uint32` — comparable, ordered, map-keyable, const-expressible
  via hex literals.
- `type Major, Layer, PkgCode, Serial uint8` — named octet types prevent
  positional-argument footguns in `Pack(m, l, p, s)`.
- `Pack(Major, Layer, PkgCode, Serial) Code` — runtime constructor.
- `(Code).Major() / Layer() / Package() / Serial()` — octet accessors.
- `(Code).String() string` — canonical `"M.L.P.S"` form (storage / logs /
  audit keys).
- `(Code).Padded() string` — display-only `"MMM.LLL.PPP.SSS"` form.
- `ParseCode(string) (Code, error)` — strict canonical parser.
- `MaskByMajor / MaskByLayer / MaskByPackage / MaskExact` — CIDR masks.
- `NewPrefixMatcher(prefix, mask Code) *PrefixMatcher` — `errors.Is` target.
- `(*Error).Is(target error) bool` — implements the errors.Is protocol.

### Wrap trail

Every `*Error` carries `trail []Code` + `trailTruncated bool`. Origin wins
for `Code()` (ADR 0002 semantics preserved); every `Wrap` call appends its
`params.Code` to the trail. Cap = 16 entries with origin-preserving
truncation and a monotonic truncation flag. Renders conditionally in
`Error()` with ASCII `<-` arrow:

```
[0.3.2.1 JSON_MARSHAL_FAILED] invalid input              (no wrap)
[0.3.2.1 <- 1.2.0.3 JSON_MARSHAL_FAILED] invalid input   (1 wrap)
[0.3.2.1 <- 1.2.0.3 (truncated) JSON_MARSHAL_FAILED] invalid input
```

Registered trail entries never contain the zero Code — a caller bug
(`params.Code == 0`) results in the trail being preserved unchanged
rather than poisoned.

### Registry (partial — full YAML at `docs/adr/0005-error-codes.yaml` in
a follow-up CI wave; this MR ships prose)

| Package | Dotted range | Used in this MR |
|---|---|---|
| `internal/kernel/errs` (meta) | `0.0.0.*` | `CodeInvalidCode=0.0.0.1`, `CodeInvalidReason=0.0.0.2`, `CodeInvalidPublic=0.0.0.3`, `CodeInvalidPrivate=0.0.0.4` NEW, `CodeInvalidCodeString=0.0.0.5` NEW, `CodeInvalidWrapParams=0.0.0.6` NEW |
| `internal/kernel/buffer` | `0.1.1.*` | reserved |
| `internal/kernel/clock` | `0.1.2.*` | reserved |
| `internal/core/logger` | `0.2.1.*` | reserved |
| `internal/core/logger/level` | `0.2.0.*` | reserved |
| `internal/core/codec` | `0.2.2.*` | `CodeDuplicateRegistration=0.2.2.1`, `CodeEmptyInput=0.2.2.2`, `CodeTargetInvalid=0.2.2.3`, `CodeValueInvalid=0.2.2.4` |
| `internal/service/logger` | `0.3.1.*` | `CodeWriterNil=0.3.1.1`, `CodeHandlerNil=0.3.1.2`, `CodeCtxCancelled=0.3.1.10`, `CodeWriteFailed=0.3.1.20` |
| `internal/service/codec/json` | `0.3.2.*` | 2 codes |
| `internal/service/codec/xml` | `0.3.3.*` | 2 codes |
| `internal/service/codec/yaml` | `0.3.4.*` | 2 codes |
| `internal/service/codec/toml` | `0.3.5.*` | 2 codes |
| `internal/service/codec/cbor` | `0.3.6.*` | 2 codes |
| `internal/service/codec/msgpack` | `0.3.7.*` | 2 codes |
| `internal/service/codec/csv` | `0.3.8.*` | 3 codes |
| `internal/service/codec/asn1` | `0.3.9.*` | 2 codes |
| `internal/service/codec/pem` | `0.3.10.*` | 3 codes |
| `internal/service/codec/ndjson` | `0.3.11.*` | 3 codes |
| `pkg/v1/logger` | `1.1.0.*` | `CodeWriterRequired=1.1.0.1` |
| `pkg/v1/codec` | `1.2.0.*` | `CodeUnknownFormat=1.2.0.1`, `CodeCodecUnavailable=1.2.0.2`, `CodeStreamingUnsupported=1.2.0.3` |
| `pkg/v1/codec/baseenc` | `1.2.1.*` | `CodeInvalidEncoding=1.2.1.1`, `CodeDecodeFailed=1.2.1.2`, `CodeEncodeFailed=1.2.1.3` |
| `pkg/v2+` | `2.*.*.*` onwards | future |

### Enforcement

- **Runtime:** `validateDefineArgs` rejects `code == 0`, `uint32(code) > 0x7FFF_FFFF`, and `Layer == 0` outside the 6-member meta-code whitelist. `newValidationError` is a bootstrap struct-literal constructor that never re-enters `Define` — init recursion is impossible by construction.
- **AST audit:** existing registry audit in `internal/kernel/errs/registry_external_test.go` walks `errs.Define` calls and enforces uniqueness + `screamingSnake(varName) == reason`. Follow-up: `tools/errs-codegen` + `tools/errs-audit` binaries that load a canonical YAML registry.
- **Build tag:** `//go:build amd64 || arm64 || riscv64 || ppc64 || ppc64le || s390x` on `internal/kernel/errs/code.go` + a static `unsafe.Sizeof(int(0))` assertion guarantees 64-bit GOARCH so the deprecated `Code() int` accessor roundtrips losslessly.

## Semantics

- `Error()` shape changes from `[1001 REASON] msg` to `[0.0.0.1 REASON] msg`.
  Regex-based log parsers must migrate to `\[[\d.]+(?: <- [\d.]+)*(?: \(truncated\))? \w+\]`.
- `Code()` returns an `int` (deprecated); value changes for every constant
  because the integer encoding of a dotted-quad is not the old flat int.
  Consumers matching on named constants keep working; consumers matching
  on numeric literals must update.
- `HasCode` takes `Code` (was `int`); an int shim `HasCodeInt` remains
  `Deprecated` until v1.0.0.
- `errors.Is(err, NewPrefixMatcher(prefix, mask))` — new idiomatic path
  for CIDR-style routing. `Matches(prefix, mask) bool` on `Code` was
  considered and dropped (YAGNI); callers can always do `c & mask`
  manually if they want to avoid the matcher allocation.

## Deferred

- `tools/errs-codegen` + `tools/errs-audit` standalone binaries (ADR 0005
  YAML fixture becomes canonical) — follow-up PR.
- `KTN-ERRS-DOTTEDQUAD` linter rule that rejects `Define(<int-literal>, …)`
  outside the migration window — follow-up PR.
- `LegacyFormat(e *Error) string` returning the pre-ADR-0005 text shape
  as a transitional bridge — follow-up PR (only ships if any consumer
  reports breakage).
- Structured-log export of the trail (`"trail": [...]` JSON field) —
  follow-up; v1 ships only the text rendering in `Error()`.

## Why not ...

- **`uint64` layout (e.g. 8/8/16/32)** — doubles memory cost for every
  sentinel without a real consumer pressuring the 256-per-package cap.
  If saturation ever happens for a package, the right move is to split
  the package, not inflate the type.
- **Struct-based Code** — breaks `const` expressiveness, breaks map keys
  of `Code`, breaks `switch` on `Code`. Not worth the ergonomics loss.
- **`Matches(prefix, mask) bool` on Code** — considered, dropped. Same
  effect via `errors.Is(err, NewPrefixMatcher(…))` with better stdlib
  integration and no additional method on `Code`.
- **Storing the trail on `Unwrap() []error` instead of a field** — would
  require every wrapper to be `*Error`, but we want stdlib errors (e.g.,
  `fmt.Errorf("%w", sdkErr)`) to continue working transparently. Field
  storage is simpler and costs 16×4 = 64 bytes at full trail.

## References

- Plan: `/workspace/.claude/plans/error-codes-dotted-quad.md` (v5, 8 iterations)
- Supersedes: `docs/adr/0002-sdk-errors-package.md` §Registry
- Current impl: `internal/kernel/errs/code.go`, `error.go`, `validate.go`,
  `trail.go`, `prefix_matcher.go`, `parse.go`, `accessors.go`
- Public facade: `pkg/v1/errs/accessors.go`
