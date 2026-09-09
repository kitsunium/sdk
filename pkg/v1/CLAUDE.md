<!-- updated: 2026-05-18T14:30:00Z -->
# pkg/v1/

## Purpose

The first major version of the SDK's public API. Type signatures exposed here are **frozen post-v1.0.0** — breaking changes land in `pkg/v2`. Today the surface is a thin alias + helper layer over `internal/core/*` and `internal/service/*` (zero runtime cost; the Go type system treats `pkg/v1/X.T` and `internal/.../T` as the same type).

## Contents

| Package | Role | README |
|---|---|---|
| `logger/slogbridge/` | `NewHandler` / `New` — adapt an SDK `Logger` to `log/slog` for APIs typed on the concrete `*slog.Logger` (ADR 0032). The one package allowed to import `log/slog` | `pkg/v1/logger/slogbridge/README.md` |
| `logger/` | Logger facade: `Config` / `NewText` / `Default` / `NewWithSink`, `Info|Warn|Error|Debug`, `Build` builder, `String|Int|…` attr ctors, `Version` (ldflags injection point) | `pkg/v1/logger/README.md` |
| `codec/` | Universal codec dispatch: `Marshal` / `Unmarshal` / `NewEncoder` / `NewDecoder` over a `Format` registry; blank-imports 16 service codecs covering 24 Format names — text/binary/base-N reached identically (asn1-der, baseenc family [base64/base64url/base32/base16/hex/ascii85/base45/base58/base62], bson, cbor, csv, flatbuffers, form, json, msgpack, multipart, ndjson, pem, tlv, toml, xml, yaml) | _(no README)_ |
| `errs/` | Error introspection **and construction** (ADR 0019): read — `CodeOf` / `ReasonOf` / `PublicOf` / `PrivateOf` / `HTTPStatusOf` / `ExitCodeOf` / `HasCode` / `HasReason` / `NewPrefixMatcher`; build — `New` / `Wrap` (+ `WrapParams`) / `Field` helpers (`String` / `Int` / `Int64` / `Bool` / `Float` / `NewFieldValue`); codes — `Pack` / `ParseCode` / `MinAppMajor` / `MaxMajor` + `Code` / `Major` / `Layer` / `PkgCode` / `Serial` / `Field` / `PrefixMatcher` type aliases + `MaskBy*` constants. Octets are composable on the typed `Code` (e.g. `code.Layer()`). | `pkg/v1/errs/README.md` |
| `id/` | Identifier generation (ADR 0024): `New(scheme)` dispatch + helpers `UUIDv4` / `UUIDv7` / `ULID` / `Snowflake` / `NanoID` / `KSUID`; configured constructors `NewSnowflake(node)` / `NewNanoID(size)` / `NewTypeID(prefix)`; decoders `ParseKSUID` / `ParseTypeID` / `FormatTypeID`; `Available`; `Scheme` constants; stdlib-only, cross-OS | `pkg/v1/id/README.md` |
| `cache/` | Generic LRU + TTL `Cache[K,V]` (ADR 0025): `New` + `Fetch` / `Set` / `SetTTL` / `Delete` / `Len` / `Purge` / `Stats`; type aliases onto `kernel/cache`; stdlib-only, cross-OS | `pkg/v1/cache/README.md` |
| `resilience/` | Reliability policies (ADR 0026): `NewRetry` / `NewCircuitBreaker` / `NewRateLimiter` / `NewBulkhead` / `NewTimeout` / `NewFallback` / `NewHedge` returning composable `Runner`s; `Operation`/`Runner` + `*Config` aliases; outcome sentinels; stdlib-only, cross-OS | `pkg/v1/resilience/README.md` |
| `metrics/` | Observability (ADR 0027): `NewMeter` → lock-free Counter/Gauge/Histogram; `Collect` → `Snapshot`; `Export`/`RegisterExporter`/`NewTextExporter`/`NewPrometheusExporter`/`AvailableExporters`; the `text` and `prometheus` exporters both register on **stderr** (ADR 0030 — stdout may be the process's protocol channel), stdout reachable via `NewTextExporter(name, os.Stdout)` and a scrape via `NewPrometheusExporter(name, w)`; stdlib-only, cross-OS | `pkg/v1/metrics/README.md` |
| `config/` | Configuration (ADR 0028): generic `Load[T]` merging `EnvSource`/`FileSource` (later wins) + decode + `Validator`; `PollWatcher` cross-OS hot-reload; stdlib-only | `pkg/v1/config/README.md` |
| `scheduler/` | Time-driven execution (ADR 0041): `Parse`/`ParseInLocation` (five-field POSIX cron, UTC by default) + `Every` + `New` → a `Scheduler` you `Add` to and `Run`; `Job`/`Schedule`/`Entry`/`Result`/`Config` aliases; every construct outside the subset refused BY NAME at construction; DST, missed deadlines and overlap documented rather than emergent; stdlib-only, cross-OS | `pkg/v1/scheduler/README.md` |
| `token/` | Security tokens (ADR 0042): JWT over JWS Compact Serialization + PASETO v4.public. One constructor per algorithm — `NewHS256Verifier` takes a `crypto.Key`, `NewES256Verifier` an `*ecdsa.PublicKey` — so algorithm confusion is a call that does not compile; `alg:none` has no representation in `Algorithm`; `exp` is required unless opted out by name; `NewSetVerifier` selects by `kid` from a JWK Set and resolves a duplicated one by signature. `Claims` prints its shape, never its values. stdlib-only | `pkg/v1/token/README.md` |

The `codec/` sub-package was added since the original CLAUDE.md. The legacy `codec/baseenc/` byte-level package was removed in favour of uniform `codec.Marshal("base64"|"base64url"|"base32"|"base16"|"hex"|"ascii85", v)` dispatch — every encoding format now goes through the same verb.

## Module

Single Go module `github.com/kitsunium/sdk/pkg` — one `go.mod` (at `pkg/go.mod`), one `go.sum`. The consumer packages live under this `v1/` directory, so import paths stay `github.com/kitsunium/sdk/pkg/v1/*`; only the module declaration sits one level up. Go forbids a `/v1` module-path suffix, so the module itself is the bare `…/pkg` (ADR 0017). Built and tested under Bazel via `//pkg/v1/...`; `cd pkg && GOWORK=off go test ./...` works for local iteration.

## Public surface contract

- **Stable identifiers.** Every exported name / type / const in `pkg/v1/*` is frozen until `pkg/v2` cuts. New helpers can be added; existing signatures cannot move.
- **Aliases, not new types.** Public types are `type X = internalPkg.X` so consumers and SDK code share the type identity (a `pkg/v1/logger.Attr` passes anywhere `corelogger.AttrValue` is expected).
- **Error model is constructable; other internal types are not.** Since ADR 0019, `pkg/v1/errs` exposes `New` / `Wrap` (+ `WrapParams`) and the `Field` helpers alongside the `Of`-accessors. Consumers receive `error`, introspect it, AND mint their own typed errors in the same model — but the concrete `*errs.Error` stays unexported, so they cannot forge one by struct literal; construction routes through the runtime-validated constructors (which return a typed `CodeInvalid*` error, never panic). The kernel `errs.Define` (panic-at-init, AST-audited) stays internal; the public path is the non-panicking `New`/`Wrap`. This exception is `errs`-only — logger/codec internals keep their constructors private.
- **`FieldValue` is passed through** (as the `Field` alias) since ADR 0019, so consumers attach structured metadata at construction. The previously-deferred `FieldsOfAsMap(err) map[string]string` read-side helper is still add-on-demand.

## Version freeze policy

- `pkg/v1` signatures freeze on first `v1.0.0` tag. Breaking signature changes after the freeze go to `pkg/v2` (coexists with v1 until deprecation).
- **Pre-1.0.0 precedent for breaking changes.** Two waves of breaking changes have already landed under `pkg/v1`:
  1. ADR 0002 errors-package MR — 4 breaking signature changes + 1 behaviour change (e.g. `NewText(Config{Writer: nil})` now returns `WriterRequired` instead of silently defaulting to stderr).
  2. PR #25 (`refactor!: drive ktn-linter phases 1-7 to zero issues`) — `baseenc.Encoding` migrated from untyped `string` constants to a typed `int` + `iota` block with `EncodingUnknown` as the zero-value sentinel. Caller code that compared `Encoding` to a string literal stopped compiling; the typed form makes typos catchable at the call site.
- Both waves shipped before `v1.0.0`. **After v1.0.0 the policy hardens: no breaking changes in `pkg/v1`** — any further migrations go to `pkg/v2`. The pre-1.0 precedent does not authorise post-1.0 breakage.
- Security fixes in `internal/*` propagate via minor bumps on the affected module without touching `pkg/v1` — the facade re-exports, it does not duplicate.

## Conventions

- Import aliases at call sites: `logger "…/pkg/v1/logger"`, `errs "…/pkg/v1/errs"`, `codec "…/pkg/v1/codec"`.
- `import _ "github.com/kitsunium/sdk/pkg/v1/codec"` is enough to activate all 16 service codecs (24 Format names incl. base-N family) — registry side-effects driven by blank imports.
- Every emitted log record carries `framework_version` via the ldflags-injected `logger.Version`. Injection recipe is in `pkg/v1/logger/README.md`; under Bazel `--stamp` + `x_defs` + `tools/workspace_status.sh` (`STABLE_VERSION`) supply the same value.
- `logger.NewText(Config{Writer: nil})` returns `(nil, WriterRequired)`. `logger.NewWithSink(SinkConfig{Sink: nil})` returns `(nil, SinkConfigRequired)`. Use `logger.Default()` for the stderr one-liner.

## Do NOT

- Expose `errs.PrivateOf(err)` output in HTTP / gRPC responses, error pages, or any user-facing surface. Diagnostic-only — documented in the godoc and `pkg/v1/errs/README.md`.
- Build a typed error with raw `errors.New` / `fmt.Errorf` when adopting the SDK error model — use `errs.New` / `errs.Wrap` (ADR 0019) so the result carries a `Code` + Public/Private split. Do NOT reach for the internal `errs.Define` (it panics at init and is `internal/`-blocked); the public `New`/`Wrap` are the runtime-validated path. Assign consumer codes a Major in `[errs.MinAppMajor, errs.MaxMajor]` (`0x40–0x7F`) to stay collision-free with the SDK.
- Set `Version` at runtime from application code — use the ldflags recipe (or Bazel `--stamp`) so every binary commits its version at link time.
- Reach for a byte-level base-N API in `pkg/v1`. There is none — `codec.Marshal("base64", v)` (or stdlib `encoding/base64` directly when you have raw bytes already) is the only path.

## Verification

```
# Primary (Bazel)
bazel test --config=race //pkg/v1/...

# Fallback (per-module; go.mod is at pkg/, code under v1/)
cd pkg
GOWORK=off go test -race -cover ./v1/...
```

## Subtree

- `logger/` — see `pkg/v1/logger/CLAUDE.md`
- `logger/slogbridge/` — see `pkg/v1/logger/slogbridge/CLAUDE.md`
- `codec/` — see `pkg/v1/codec/CLAUDE.md`
- `errs/` — see `pkg/v1/errs/CLAUDE.md`
- `id/` — see `pkg/v1/id/CLAUDE.md`
- `cache/` — see `pkg/v1/cache/CLAUDE.md`
- `resilience/` — see `pkg/v1/resilience/CLAUDE.md`
- `metrics/` — see `pkg/v1/metrics/CLAUDE.md`
- `config/` — see `pkg/v1/config/CLAUDE.md`
- `scheduler/` — see `pkg/v1/scheduler/CLAUDE.md`
