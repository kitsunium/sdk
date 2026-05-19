<!-- updated: 2026-05-18T14:30:00Z -->
# pkg/

## Purpose

The SDK's stable public API surface. Each subdirectory is a major version (`v1`, `v2`, …). Consumers import `pkg/<major>/*`; `internal/*` is blocked by Go's `internal/` rule AND by Bazel layer visibility (ADR 0004).

## Contents

| Major | Purpose | State |
|---|---|---|
| `v1/` | Stable public API for logging, error introspection, and codec dispatch (13 codecs covering 18 Format names — incl. base-N family via uniform `codec.Marshal`/`Unmarshal`) | Shipping |

## Versioning policy

- `pkg/v1` signatures are **frozen post-v1.0.0**. Any breaking change goes into a new `pkg/v2` (coexists with v1 until deprecation).
- Security fixes in `internal/*` propagate via minor bumps on the module concerned — no `pkg/v1` changes required because it only re-exports.
- Adding a new `pkg/vN` is a dedicated ADR.

## Conventions

- Public types are **type aliases** onto `internal/core/*` / `internal/kernel/*` (clean short names, zero runtime cost). Example: `Attr = AttrValue`.
- Public functions are thin wrappers: validation + delegation. No business logic in this layer.
- **No constructors for internal types.** Consumers can introspect SDK errors via `pkg/v1/errs.*Of(err)` accessors (`CodeOf`, `ReasonOf`, `PublicOf`, `PrivateOf`, `LayerOf`, `HTTPStatusOf`, `ExitCodeOf`, `HasCode`, `HasReason`) but cannot forge `*errs.Error` values.
- **ldflags injection.** `pkg/v1/logger.Version` is the single injection point; all other packages read via `logger.FrameworkVersion()`, which falls back to the `"dev"` sentinel when unset.
- **`pkg/v1/codec` blank-imports all 13 service codec packages** so `import _ "github.com/kitsunium/sdk/pkg/v1/codec"` activates the full registry (asn1, baseenc, cbor, csv, flatbuffers, json, msgpack, ndjson, pem, tlv, toml, xml, yaml). The baseenc package registers 6 distinct Format names (`base64`, `base64url`, `base32`, `base16`, `hex`, `ascii85`).
- **Uniform dispatch.** Every encoding format — text, binary, base-N — is reached the same way: `codec.Marshal(format, v)` / `codec.Unmarshal(data, &v)`. Format-swap at runtime is a single string change. The SDK does NOT ship a parallel byte-level API; callers needing raw base-N bytes without the JSON envelope call stdlib `encoding/{base64,base32,hex,ascii85}` directly.

## Subtree

- `v1/` — see `pkg/v1/CLAUDE.md`

## Do NOT

- Export the concrete `*errs.Error` type here; consumers should see `error` only.
- Import from `pkg/v1` into `internal/*`. The public facade sits at the top of the dependency graph.
- Rename or remove an exported identifier in `pkg/v1/*` without cutting `pkg/v2`.
- Surface `PrivateOf(err)` output in HTTP/gRPC responses — Private is diagnostic-only.
- Set `Version` at runtime from application code; use the ldflags recipe (or Bazel `--stamp`) so every binary commits its version at link time.

## Verification

```
bazel test --config=race //pkg/...
# Fallback per-module:
cd pkg/v1 && GOWORK=off go test -race -cover ./...
```
