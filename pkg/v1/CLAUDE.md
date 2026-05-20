<!-- updated: 2026-05-18T14:30:00Z -->
# pkg/v1/

## Purpose

The first major version of the SDK's public API. Type signatures exposed here are **frozen post-v1.0.0** — breaking changes land in `pkg/v2`. Today the surface is a thin alias + helper layer over `internal/core/*` and `internal/service/*` (zero runtime cost; the Go type system treats `pkg/v1/X.T` and `internal/.../T` as the same type).

## Contents

| Package | Role | README |
|---|---|---|
| `logger/` | Logger facade: `Config` / `NewText` / `Default` / `NewWithSink`, `Info|Warn|Error|Debug`, `Build` builder, `String|Int|…` attr ctors, `Version` (ldflags injection point) | `pkg/v1/logger/README.md` |
| `codec/` | Universal codec dispatch: `Marshal` / `Unmarshal` / `NewEncoder` / `NewDecoder` over a `Format` registry; blank-imports 13 service codecs covering 18 Format names — text/binary/base-N reached identically (asn1-der, baseenc family [base64/base64url/base32/base16/hex/ascii85], cbor, csv, flatbuffers, json, msgpack, ndjson, pem, tlv, toml, xml, yaml) | _(no README)_ |
| `errs/` | Read-only error introspection: `CodeOf` / `ReasonOf` / `PublicOf` / `PrivateOf` / `HTTPStatusOf` / `ExitCodeOf` / `HasCode` / `HasReason` / `NewPrefixMatcher` / `Pack` / `ParseCode` + `Code` / `Major` / `Layer` / `PkgCode` / `Serial` / `PrefixMatcher` type aliases + `MaskBy*` constants. Octets are composable on the typed `Code` (e.g. `code.Layer()`). | `pkg/v1/errs/README.md` |

The `codec/` sub-package was added since the original CLAUDE.md. The legacy `codec/baseenc/` byte-level package was removed in favour of uniform `codec.Marshal("base64"|"base64url"|"base32"|"base16"|"hex"|"ascii85", v)` dispatch — every encoding format now goes through the same verb.

## Module

Single Go module `github.com/kitsunium/sdk/pkg/v1` — one `go.mod`, one `go.sum`. Built and tested under Bazel via `//pkg/v1/...`; `cd pkg/v1 && GOWORK=off go test ./...` works for local iteration.

## Public surface contract

- **Stable identifiers.** Every exported name / type / const in `pkg/v1/*` is frozen until `pkg/v2` cuts. New helpers can be added; existing signatures cannot move.
- **Aliases, not new types.** Public types are `type X = internalPkg.X` so consumers and SDK code share the type identity (a `pkg/v1/logger.Attr` passes anywhere `corelogger.AttrValue` is expected).
- **No constructors for internal types.** `pkg/v1/errs` exposes `Of`-accessors only — consumers receive `error` and introspect; they cannot forge `*errs.Error`. `errs.Define` and `errs.Wrap` are intentionally NOT re-exported.
- **No pass-through of `FieldValue`** at v1: if consumer demand surfaces we add `FieldsOfAsMap(err) map[string]string` in a minor bump.

## Version freeze policy

- `pkg/v1` signatures freeze on first `v1.0.0` tag. Breaking signature changes after the freeze go to `pkg/v2` (coexists with v1 until deprecation).
- **Pre-1.0.0 precedent for breaking changes.** Two waves of breaking changes have already landed under `pkg/v1`:
  1. ADR 0002 errors-package MR — 4 breaking signature changes + 1 behaviour change (e.g. `NewText(Config{Writer: nil})` now returns `WriterRequired` instead of silently defaulting to stderr).
  2. PR #25 (`refactor!: drive ktn-linter phases 1-7 to zero issues`) — `baseenc.Encoding` migrated from untyped `string` constants to a typed `int` + `iota` block with `EncodingUnknown` as the zero-value sentinel. Caller code that compared `Encoding` to a string literal stopped compiling; the typed form makes typos catchable at the call site.
- Both waves shipped before `v1.0.0`. **After v1.0.0 the policy hardens: no breaking changes in `pkg/v1`** — any further migrations go to `pkg/v2`. The pre-1.0 precedent does not authorise post-1.0 breakage.
- Security fixes in `internal/*` propagate via minor bumps on the affected module without touching `pkg/v1` — the facade re-exports, it does not duplicate.

## Conventions

- Import aliases at call sites: `logger "…/pkg/v1/logger"`, `errs "…/pkg/v1/errs"`, `codec "…/pkg/v1/codec"`.
- `import _ "github.com/kitsunium/sdk/pkg/v1/codec"` is enough to activate all 13 service codecs (18 Format names incl. base-N family) — registry side-effects driven by blank imports.
- Every emitted log record carries `framework_version` via the ldflags-injected `logger.Version`. Injection recipe is in `pkg/v1/logger/README.md`; under Bazel `--stamp` + `x_defs` + `tools/workspace_status.sh` (`STABLE_VERSION`) supply the same value.
- `logger.NewText(Config{Writer: nil})` returns `(nil, WriterRequired)`. `logger.NewWithSink(SinkConfig{Sink: nil})` returns `(nil, SinkConfigRequired)`. Use `logger.Default()` for the stderr one-liner.

## Do NOT

- Expose `errs.PrivateOf(err)` output in HTTP / gRPC responses, error pages, or any user-facing surface. Diagnostic-only — documented in the godoc and `pkg/v1/errs/README.md`.
- Try to construct a `*errs.Error` from consumer code. Go's `internal/` rule blocks it AND the design is deliberate.
- Set `Version` at runtime from application code — use the ldflags recipe (or Bazel `--stamp`) so every binary commits its version at link time.
- Reach for a byte-level base-N API in `pkg/v1`. There is none — `codec.Marshal("base64", v)` (or stdlib `encoding/base64` directly when you have raw bytes already) is the only path.

## Verification

```
# Primary (Bazel)
bazel test --config=race //pkg/v1/...

# Fallback (per-module)
cd pkg/v1
GOWORK=off go test -race -cover ./...
```

## Subtree

- `logger/` — see `pkg/v1/logger/CLAUDE.md`
- `codec/` — see `pkg/v1/codec/CLAUDE.md`
- `errs/` — see `pkg/v1/errs/CLAUDE.md`
