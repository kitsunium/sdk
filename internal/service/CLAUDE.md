<!-- updated: 2026-05-21T21:27:56Z -->
# internal/service/

## Purpose

Concrete implementations of the contracts declared in `internal/core/*`. This is where actual I/O, formatting, locking, codec dispatch, and error wrapping happen. The `pkg/v1/*` facade wraps service constructors and hides the wiring from consumers.

## Contents

| Sub-tree | Purpose | Code prefix |
|---|---|---|
| `logger/` | v2 one-alloc-per-emit multi-sink architecture (`builder`, `encoder`, `sink/{console,file,syslog,memory}`, `middleware/{multi,async,route,failover,sample,recover,encwrite,tee}`) realising `core/logger.Handler` + `Logger` | `0.3.1.*` (and per-component slots, see logger CLAUDE.md) |
| `codec/` | 16 wire-format codecs over 24 Format names (asn1, baseenc[9], bson, cbor, csv, flatbuffers, form, json, msgpack, multipart, ndjson, pem, tlv, toml, xml, yaml), each implementing `core/codec.Codec`; all satisfy `Appender`, most also `StreamingCodec` | `0.3.2.*` … `0.3.40.*` (one PP slot per codec) |
| `crypto/` | stdlib-only schemes behind the eight `core/crypto` ports (aesgcm, streamaead, stdhash, hmacsha2, ecdsasig, ed25519sig, hkdfsha256, pbkdf2pw, x25519), the keyenvelope/keytree compositions, and the `jwk` key format (RFC 7517 JWK + JWK Set) | schemes emit core sentinels `0.2.4.*`; `jwk` owns `0.3.42.*` |
| `id/` | identifier generators (UUIDv4/v7, ULID, snowflake, NanoID, KSUID, TypeID) implementing `core/id.Generator`; stdlib-only, cross-OS, self-registered except TypeID (ADR 0024) | `0.3.39.*` |
| `resilience/` | concrete reliability policies (retry/circuit-breaker/rate-limit/bulkhead/timeout/fallback/hedging) implementing `core/resilience.Runner`; stdlib + kernel clock, cross-OS (ADR 0026) | (emits core sentinels `0.2.8.*`) |
| `metrics/` | in-memory Meter + lock-free instruments (both counter kinds, gauge, histogram, and the three observable families) + a lossless `text` exporter, a deliberately lossy `prometheus` connector and the native **OTLP/JSON** wire — encoder (`EncodeOTLPJSON`) and OTLP/HTTP emitter, written from the specification with zero OTel or protobuf dependencies (ADR 0048) — implementing `core/metrics`; TYPED attribute sets with a per-name cardinality bound and an aggregated overflow series; delta temporality makes `Collect` consume the window it reports; allocation-free lookup (see its `BENCH.md`); stdlib-only, cross-OS (ADR 0027, ADR 0044) | `0.3.45.*` (+ core sentinels `0.2.9.*`) |
| `config/` | env+file sources, merge+decode+validate `Load[T]`, cross-OS poll watcher, implementing `core/config`; codec-dispatched file parse (ADR 0028) | (emits core sentinels `0.2.10.*`) |
| `scheduler/` | five-field POSIX cron parser + fixed-interval `Every` + the firing engine, implementing `core/scheduler`; waits through `kernel/clock.Timed`, never package `time` (ADR 0041) | parser owns `0.3.43.*`; the engine emits core sentinels `0.2.12.*` |
| `lifecycle/` | the ordering engine implementing `core/lifecycle`, plus `Run` — the opt-in bridge to `proc/{signal,sdnotify}`. The partial-start unwind and an ordinary `Stop` share ONE function (`stopEach`), over a list appended to before the next `Start` is attempted, with contexts detached by `WithoutCancel` so a start aborted by cancellation still gets a real cleanup; the component that FAILED is never stopped. The shutdown budget is per component, so the first thing that will not finish cannot spend everyone else's — expiry cancels that `Stop`'s context, records `StopTimeout`, and continues, killing no goroutine and closing nothing the component owns. Waits through `kernel/clock.Timed`, never package `time`, enforced by a named AST audit over production code AND the suite (ADR 0050) | `0.3.49.*` (plus core sentinels `0.2.19.*`) |
| `token/` | JWT over JWS Compact Serialization + PASETO v4.public, implementing `core/token`; composes `crypto/{hmacsha2,ed25519sig,jwk}`, plus `crypto/ecdsa` directly for the JOSE fixed-width R\|\|S encoding `ecdsasig`'s DER cannot supply; the algorithm is bound by the constructor and never read from the token (ADR 0042) | `0.3.44.*` (plus core sentinels `0.2.13.*`) |
| `validation/` | the constraint engine implementing `core/validation`: built-in constraints, the reflection-free `Field`/`Each` combinators, and the `Struct[T]` struct-tag front end whose compiled plan is cached per `(type, mode)` — 48× cheaper than recompiling, see its `BENCH.md`; every dialect construct the SDK declines is refused BY NAME at compile time (ADR 0046) | `0.3.47.*` (plus core sentinels `0.2.15.*`) |
| `cache/` | the tagged, stampede-protected memory store over the `kernel/cache` primitive plus the L1/L2 `NewChain`, implementing `core/cache`; composes `kernel/singleflight` (one fill per key per PROCESS — not across replicas) and `kernel/clock`. One mutex covers the primitive AND the tag index, `Fetch` takes it exclusively because a hit mutates, and `onEvict` takes no lock at all since it runs on the caller's goroutine inside a held one. Tag invalidation is O(k) in the tagged entries, not O(N) in the store — 1.16× across a 100× store, measured in its `BENCH.md`. The chain refuses at construction any tier that cannot return its entry, because a promotion without tags is a copy `InvalidateTag` can never reach (ADR 0049) | `0.3.48.*` (plus core sentinels `0.2.18.*`) |
| `lock/` | the in-process locker (leases that DO expire, with takeover and a per-name fence ledger) plus the `flock(2)` file locker (leases that do NOT, because the kernel already releases a dead process's lock) and the `Keepalive` that cancels a derived context when a renewal fails, implementing `core/lock`; composes `kernel/clock.Timed` for every deadline and every retry, so nothing in the package — production or tests — waits on the wall clock. The file locker takes an in-process gate BEFORE its `flock`: `flock(2)` is per open file description, so re-locking a shared descriptor is a lock conversion that returns at once, and eight goroutines sharing one descriptor were measured inside one counted section simultaneously, every run. It never blocks in `flock` (no cancellation reaches a parked syscall), refuses a world-writable non-sticky directory (unlinking the lock file hands the next process a different inode), and refuses `proc.UnsupportedPlatform` at construction where `flock(2)` does not exist (ADR 0052/0018) | `0.3.51.*` (plus core sentinels `0.2.21.*`) |
| `session/` | memory store + file store + AEAD cookie sealer, implementing `core/session`; composes `crypto/aesgcm` and `kernel/clock` (the narrow `Clock` half — a store reads time, it never waits). The file store seals every record, binds it to its own filename through the AAD, narrows **and then stats** its permissions, publishes by `rename(2)`, and serialises under one store-wide `flock`; where those mechanics do not exist it refuses at construction with `proc.UnsupportedPlatform` rather than pretending (ADR 0045/0018) | `0.3.46.*` (plus core sentinels `0.2.14.*`) |
| `trace/` | the `Tracer`, the recording span, four samplers, the in-memory `Recorder`, the `RecordError` helper, the **OTLP/JSON** encoder + OTLP/HTTP emitter for `/v1/traces`, and the two HTTP middlewares — implementing `core/trace` from the OpenTelemetry and W3C documents with zero `go.opentelemetry.io` imports. Trace and span identifiers ride as **hex**, not base64: the one place OTLP overrides the protobuf-JSON mapping, and the one a generated encoder gets wrong silently. `Ratio` REFUSES a rate of exactly 0, because that value also spells "unconfigured" (ADR 0031). `ServerMiddleware` / `ClientMiddleware` are `core/net`'s own generic middleware type instantiated at `http.Handler` / `http.RoundTripper`, and the status wrapper implements `Unwrap` and nothing else, so it cannot LIE about Flush/Hijack the way ADR 0047 fixed in the engine. The OTLP/HTTP emitter is never registered (ADR 0051) | `0.3.50.*` (plus core sentinels `0.2.20.*`) |

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
- `codec/` — see `internal/service/codec/CLAUDE.md` (16 codec packages + per-codec error ranges)
- `crypto/` — see `internal/service/crypto/CLAUDE.md` (scheme packages, the keyenvelope/keytree compositions, and the `jwk` key format)
- `id/` — see `internal/service/id/CLAUDE.md` (UUIDv4/v7, ULID, snowflake, NanoID, KSUID, TypeID — ADR 0024)
- `resilience/` — see `internal/service/resilience/CLAUDE.md` (retry/breaker/ratelimit/bulkhead/timeout/fallback/hedging — ADR 0026)
- `metrics/` — see `internal/service/metrics/CLAUDE.md` (in-memory meter + the text exporter, the lossy Prometheus connector and the lossless OTLP/JSON wire — ADR 0027 / ADR 0044 / ADR 0048)
- `config/` — see `internal/service/config/CLAUDE.md` (env+file loader + poll watcher — ADR 0028)
- `scheduler/` — see `internal/service/scheduler/CLAUDE.md` (cron parser + engine — ADR 0041)
- `lifecycle/` — see `internal/service/lifecycle/CLAUDE.md` (the three mechanics behind the partial-start unwind, what an expired budget does and deliberately does not do, and the opt-in `Run` — ADR 0050)
- `token/` — see `internal/service/token/CLAUDE.md` (JWT + PASETO v4.public, and the RFC 8725 coverage table — ADR 0042)
- `validation/` — see `internal/service/validation/CLAUDE.md` (constraints, combinators, the struct-tag plan cache — ADR 0046)
- `session/` — see `internal/service/session/CLAUDE.md` (memory + file stores, the sealer, and the platform matrix — ADR 0045)
- `lock/` — see `internal/service/lock/CLAUDE.md` (both lockers, the measured `flock` intra-process behaviour that forces the in-process gate, the platform matrix, and why one backend expires and the other does not — ADR 0052)
- `trace/` — see `internal/service/trace/CLAUDE.md` (tracer, samplers, recorder, the OTLP/JSON wire and the two middlewares — ADR 0051)
- `cache/` — see `internal/service/cache/CLAUDE.md` (the tagged memory store, the four removal paths, the chain's ordering rules, and why stampede protection stops at the process boundary — ADR 0049)

## Verification

```
# Primary (Bazel)
bazel test --config=race //internal/service/...

# Fallback (go test)
cd internal/service
GOWORK=off go test -race -cover ./...
```
