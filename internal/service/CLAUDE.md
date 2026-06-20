<!-- updated: 2026-05-21T21:27:56Z -->
# internal/service/

## Purpose

Concrete implementations of the contracts declared in `internal/core/*`. This is where actual I/O, formatting, locking, codec dispatch, and error wrapping happen. The `pkg/v1/*` facade wraps service constructors and hides the wiring from consumers.

## Contents

| Sub-tree | Purpose | Code prefix |
|---|---|---|
| `logger/` | v2 zero-alloc multi-sink architecture (`builder`, `encoder`, `sink/{console,file,syslog,memory}`, `middleware/{multi,async,route,failover,sample,recover,encwrite,tee}`) realising `core/logger.Handler` + `Logger` | `0.3.1.*` (and per-component slots, see logger CLAUDE.md) |
| `codec/` | 13 wire-format codecs over 18 Format names (asn1, baseenc[6], cbor, csv, flatbuffers, json, msgpack, ndjson, pem, tlv, toml, xml, yaml), each implementing `core/codec.Codec`; all satisfy `Appender`, most also `StreamingCodec` | `0.3.2.*` … `0.3.24.*` (one PP slot per codec) |

## Module

Single module `github.com/kitsunium/sdk/internal/service` — one `go.mod` shared by **both** logger and codec sub-trees. `replace` directives resolve `../kernel` and `../core` locally so `GOWORK=off go build ./...` works per-module in CI.

## Conventions

- **Imports allowed**: stdlib + `internal/kernel/*` + `internal/core/*` + third-party libraries that codec wrappers delegate to (e.g. `github.com/fxamacker/cbor/v2`, `gopkg.in/yaml.v3`). Never `pkg/*`.
- **Concurrent safety**: every public type honours the "safe for concurrent use" contract inherited from the core interface it implements. Codec singletons are stateless; logger handlers serialise writes via `sync.Mutex` or async ring buffer.
- **Error wrapping**: `errs.Wrap(cause, WrapParams{…})` when the cause is a stdlib / third-party error; the `errs.WrapParams` fields are SILENTLY IGNORED when the cause is already an `*errs.Error` (origin wins). See `internal/kernel/errs/README.md`.
- **Codec registration**: each codec exports `var Codec codec.Codec = codec.Register(&xxxCodec{})` at package load — no `init()`. Blank-importing the package is enough to make it resolvable by Name / MIME / Extension.
- **Function length**: `KTN-FUNC-MAXLOC` caps every function at 50 lines. Helpers are split out (e.g. `escapeFormulaCells` / `escapeFormulaRow` in `codec/csv`, `renderLine` / `writeLine` in `logger`).

## Do NOT

- Re-export a service type as the public-facing API. The public facade is `pkg/v1/*` — consumers should never see `svccodec.jsonCodec` or `svclogger.builder` types directly.
- Call `fmt.Errorf` / `errors.New` in production. All errors flow through `errs.Define` (sentinels in `errors.go`) + `errs.Wrap` (call sites in `codec.go` / handler code).
- Reach into `core/*` structs to mutate them. Domain values (`AttrValue`, `RecordEvent`) are immutable after construction.
- Swallow a third-party encode/decode error silently. Wrap it via `errs.Wrap` so `errors.Is(err, originalCause)` keeps working and the dotted-quad code surfaces.
- Add an `init()` function to register a codec — the package-level `var Codec = codec.Register(...)` initialiser is the convention.

## Subtree

- `logger/` — see `internal/service/logger/README.md` (full contract, error catalogue, output format)
- `codec/` — see `internal/service/codec/CLAUDE.md` (13 codec packages + per-codec error ranges)

## Verification

```
# Primary (Bazel)
bazel test --config=race //internal/service/...

# Fallback (go test)
cd internal/service
GOWORK=off go test -race -cover ./...
```
