<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/

> Reference moved to `pkg/v1/NOTES.md` (ADR 0253), under the same section titles: Contents.

## Purpose

The first major version of the SDK's public API. Type signatures exposed here are **frozen post-v1.0.0** — breaking changes land in `pkg/v2`. Today the surface is a thin alias + helper layer over `internal/core/*` and `internal/service/*` (zero runtime cost; the Go type system treats `pkg/v1/X.T` and `internal/.../T` as the same type). Every alias, re-exported constant and variable, and forwarder is the design's `facade:` and kit writes it into each package's `gen_facade.go` (ADR 0166); a package's hand-written files keep its package comment and the functions with a body of their own.

## Module

Each family directory here is a Go module of its own (ADR 0171), `github.com/kitsunium/sdk/pkg/v1/<family>`, tagged `pkg/v1/<family>/vX.Y.Z`: import paths are `github.com/kitsunium/sdk/pkg/v1/*`, and a consumer requires the family modules it imports. A family requires `internal` and the families below it in `design/sdk.yaml`'s order (`errs`, `clock` < `collections`, `crypto`, `net`, `proc`, `concur` < `observe` < `data` < `security` < `app`), its tests included, never one above it. From ADR 0162 to ADR 0171 the packages here were directories of the root module; until ADR 0162 the bare module `…/pkg` (ADR 0017), tagged `pkg/vX.Y.Z`. Built and tested under Bazel via `//pkg/v1/...`; `go test ./pkg/v1/...` in the workspace, or `cd pkg/v1/<family> && GOWORK=off go test ./...`, for local iteration.

## Public surface contract

- **Stable identifiers.** Every exported name / type / const in `pkg/v1/*` is frozen until `pkg/v2` cuts. New helpers can be added; existing signatures cannot move.
- **Aliases, not new types.** Public types are `type X = internalPkg.X` so consumers and SDK code share the type identity (a `pkg/v1/observe/logger.Attr` passes anywhere `corelogger.AttrValue` is expected).
- **Forwarders, not function variables.** A public function is `func F(…) … { return internalPkg.F(…) }`, never `var F = internalPkg.F`. A variable can be reassigned for the whole process by any consumer, and a call through it is indirect, which hides the callee from the inliner, escape analysis and constant folding; a forwarder small enough to inline costs nothing at all, the call compiling to the callee's own body or a direct call to it. Values — sentinels, constants, `clock.System` — are still re-exported as values. Of the 22 function variables `pkg/v1` published (18 in `errs`, 4 in `observe/trace`), 21 became forwarders in one v0 change, each decided by measurement (`pkg/v1/errs/CLAUDE.md`, both `BENCH.md` files); `errs.ReasonOf` stayed a variable, its forwarder measured slower than the 3 % the rule allows, so a go/types scan of the 87 packages finds exactly one exported variable of function type, and it is that one.
- **Error model is constructable; other internal types are not.** Since ADR 0019, `pkg/v1/errs` exposes `New` / `Wrap` (+ `WrapParams`) and the `Field` helpers alongside the `Of`-accessors. Consumers receive `error`, introspect it, AND mint their own typed errors in the same model — but the concrete `*errs.Error` stays unexported, so they cannot forge one by struct literal; construction routes through the runtime-validated constructors (which return a typed `CodeInvalid*` error, never panic). The kernel `errs.Define` (panic-at-init, AST-audited) stays internal; the public path is the non-panicking `New`/`Wrap`. This exception is `errs`-only — logger/codec internals keep their constructors private.
- **`FieldValue` is passed through** (as the `Field` alias) since ADR 0019, so consumers attach structured metadata at construction, and **`FieldsOf` is re-exported** so they read it back: a `Field` answers `Key()` and `StringValue()`. The re-export is verbatim, like every other accessor; the `FieldsOfAsMap(err) map[string]string` shape is still not offered: a key can appear at several depths of a chain, and a map would pick one silently.

## Version freeze policy

- `pkg/v1` signatures freeze on first `v1.0.0` tag. Breaking signature changes after the freeze go to `pkg/v2` (coexists with v1 until deprecation).
- **Pre-1.0.0 precedent for breaking changes.** Breaking changes land under `pkg/v1` while the module is v0 — ADR 0040 makes that a licence the commit must state, and it expires at v1. Among them:
  1. ADR 0002 errors-package MR — 4 breaking signature changes + 1 behaviour change (e.g. `NewText(Config{Writer: nil})` now returns `WriterRequired` instead of silently defaulting to stderr).
  2. PR #25 (`refactor!: drive ktn-linter phases 1-7 to zero issues`) — `baseenc.Encoding` migrated from untyped `string` constants to a typed `int` + `iota` block with `EncodingUnknown` as the zero-value sentinel. Caller code that compared `Encoding` to a string literal stopped compiling; the typed form makes typos catchable at the call site.
  3. PR #27 deleted the byte-level `codec/baseenc/` package itself (and `EncodingUnknown` with it) in favour of `codec.Marshal` dispatch — see Contents above.
  4. ADR 0044 re-shaped the published `metrics` types onto the OpenTelemetry data model, citing ADR 0040.
  5. The forwarders change turned 21 function variables of `errs` (`CodeOf`, `PublicOf`, `PrivateOf`, `HTTPStatusOf`, `ExitCodeOf`, `FieldsOf`, `HasCode`, `HasReason`, `NewPrefixMatcher`, `Pack`, `ParseCode`, `String`, `Int`, `Int64`, `Bool`, `Float`, `NewFieldValue`) and `observe/trace` (`String`, `Bool`, `Int64`, `Float64`) into functions, citing ADR 0040 — `errs.ReasonOf` stayed a variable by measurement: a consumer that assigned to one of the 21 or took its address stops compiling; every call and every use as a function value compiles unchanged.
- All of them shipped before `v1.0.0`. **After v1.0.0 the policy hardens: no breaking changes in `pkg/v1`** — any further migrations go to `pkg/v2`. The pre-1.0 precedent does not authorise post-1.0 breakage.
- Security fixes in `internal/*` ship in the SDK module's next release without touching `pkg/v1` — the facade re-exports, it does not duplicate.

## Conventions

- Import aliases at call sites: `logger "…/pkg/v1/observe/logger"`, `errs "…/pkg/v1/errs"`, `codec "…/pkg/v1/data/codec"`.
- `import _ "github.com/kitsunium/sdk/pkg/v1/data/codec"` is enough to activate all 16 service codecs (24 Format names incl. base-N family) — registry side-effects driven by the blank imports of the per-format packages; `import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/<format>"` activates one and links nothing of the others.
- Every emitted log record carries `framework_version` via the ldflags-injected `logger.Version`. Injection recipe is in `pkg/v1/observe/logger/README.md`; under Bazel `--stamp` + `x_defs` + `tools/workspace_status.sh` (`STABLE_VERSION`) supply the same value.
- `logger.NewText(Config{Writer: nil})` returns `(nil, WriterRequired)`. `logger.NewWithSink(SinkConfig{Sink: nil})` returns `(nil, SinkConfigRequired)`. Use `logger.Default()` for the stderr one-liner.

## Rules from ADR 0138

Superseded by ADR 0154 (the charter); ADR 0138 stays as the incident's record, and its rules live here.

- **A same-package doc link resolves or it is not written**, judged on every
  platform that compiles its file; `make doclinks` (`tools/genindex
  -check-doclinks`, run by `make lint-check`) fails at the line.
- **A facade links the alias and names the member after it: `[Type].Member`.**
  `go/doc` does not collect the methods and fields of an alias, so
  `[Type.Member]` over one renders as literal brackets; and a link never
  qualifies into `internal/` (`[corequeue.Broker.Publish]`) in the one surface
  written for consumers.
- *Lesson*: 152 doc links named no symbol and rendered as bracketed text on
  pkg.go.dev and in the generated READMEs, with nothing failing.

## Do NOT

- Expose `errs.PrivateOf(err)` output in HTTP / gRPC responses, error pages, or any user-facing surface. Diagnostic-only — documented in the godoc and `pkg/v1/errs/README.md`.
- Build a typed error with raw `errors.New` / `fmt.Errorf` when adopting the SDK error model — use `errs.New` / `errs.Wrap` (ADR 0019) so the result carries a `Code` + Public/Private split. Do NOT reach for the internal `errs.Define` (it panics at init and is `internal/`-blocked); the public `New`/`Wrap` are the runtime-validated path. Assign consumer codes a Major in `[errs.MinAppMajor, errs.MaxMajor]` (`0x40–0x7F`) to stay collision-free with the SDK.
- Set `Version` at runtime from application code — use the ldflags recipe (or Bazel `--stamp`) so every binary commits its version at link time.
- Reach for a byte-level base-N API in `pkg/v1`. There is none — `codec.Marshal("base64", v)` (or stdlib `encoding/base64` directly when you have raw bytes already) is the only path.

## Verification

```
# Primary (Bazel)
bazel test --config=race //pkg/v1/...

# Fallback (go test; one go.mod per family)
for m in pkg/v1/*/go.mod; do (cd "${m%/go.mod}" && GOWORK=off go test -race -cover ./...); done
```

## Subtree

- `observe/` — see `pkg/v1/observe/CLAUDE.md` (the observe family — `logger`, `metrics`, `trace`, `profiling` — and the rule that put them together; a directory with no Go code, so there is no `pkg/v1/observe` to import)
- `data/` — see `pkg/v1/data/CLAUDE.md` (the data family — `codec`, `transform`, `sql`, `docstore`, `queue`, `cache`, `vfs`, `semver` — and the rule that put them together; a directory with no Go code, so there is no `pkg/v1/data` to import)
- `app/` — see `pkg/v1/app/CLAUDE.md` (the app family — `config`, `cli`, `i18n`, `validation`, `view`, `events`, `scheduler`, `statemachine`, `resilience`, `lifecycle`, `health`, `lock`, `id`, `mail`, `forward` — and the rule that put them together; a directory with no Go code, so there is no `pkg/v1/app` to import)
- `observe/logger/` — see `pkg/v1/observe/logger/CLAUDE.md`
- `app/mail/` — see `pkg/v1/app/mail/CLAUDE.md`
- `app/mail/spool/` — see `pkg/v1/app/mail/spool/CLAUDE.md`
- `app/forward/` — see `pkg/v1/app/forward/CLAUDE.md`
- `observe/logger/slogbridge/` — see `pkg/v1/observe/logger/slogbridge/CLAUDE.md`
- `data/codec/` — see `pkg/v1/data/codec/CLAUDE.md`
- `data/transform/` — see `pkg/v1/data/transform/CLAUDE.md`
- `errs/` — see `pkg/v1/errs/CLAUDE.md`
- `app/events/` — see `pkg/v1/app/events/CLAUDE.md`
- `app/id/` — see `pkg/v1/app/id/CLAUDE.md`
- `app/i18n/` — see `pkg/v1/app/i18n/CLAUDE.md`
- `data/cache/` — see `pkg/v1/data/cache/CLAUDE.md`
- `clock/` — see `pkg/v1/clock/CLAUDE.md`
- `data/semver/` — see `pkg/v1/data/semver/CLAUDE.md`
- `concur/` — see `pkg/v1/concur/CLAUDE.md` (the concurrency family — `group`, `singleflight`, `worker`, `batcher`, `snapshot`, `recycler` — pure aliases of `internal/kernel/concur`; a directory with no Go code, so there is no `pkg/v1/concur` to import)
- `concur/group/`, `concur/singleflight/`, `concur/worker/`, `concur/batcher/`, `concur/snapshot/`, `concur/recycler/` — see their `CLAUDE.md`
- `collections/` — see `pkg/v1/collections/CLAUDE.md` (the data-structure family — `heap`, `ring` — pure aliases of `internal/kernel/collections`; a directory with no Go code)
- `collections/heap/`, `collections/ring/` — see their `CLAUDE.md`
- `app/cli/` — see `pkg/v1/app/cli/CLAUDE.md`
- `security/authz/` — see `pkg/v1/security/authz/CLAUDE.md`
- `app/resilience/` — see `pkg/v1/app/resilience/CLAUDE.md`
- `observe/metrics/` — see `pkg/v1/observe/metrics/CLAUDE.md`
- `observe/metrics/goruntime/` — see `pkg/v1/observe/metrics/goruntime/CLAUDE.md`
- `observe/metrics/statsbridge/` — see `pkg/v1/observe/metrics/statsbridge/CLAUDE.md`
- `data/queue/` — see `pkg/v1/data/queue/CLAUDE.md`
- `data/stream/` — see `pkg/v1/data/stream/CLAUDE.md`
- `observe/trace/` — see `pkg/v1/observe/trace/CLAUDE.md`
- `app/config/` — see `pkg/v1/app/config/CLAUDE.md`
- `app/scheduler/` — see `pkg/v1/app/scheduler/CLAUDE.md`
- `app/lifecycle/` — see `pkg/v1/app/lifecycle/CLAUDE.md`
- `security/token/` — see `pkg/v1/security/token/CLAUDE.md`
- `app/validation/` — see `pkg/v1/app/validation/CLAUDE.md`
- `data/vfs/` — see `pkg/v1/data/vfs/CLAUDE.md`
- `app/view/` — see `pkg/v1/app/view/CLAUDE.md`
- `security/secret/` — see `pkg/v1/security/secret/CLAUDE.md`
- `security/session/` — see `pkg/v1/security/session/CLAUDE.md`
- `data/sql/` — see `pkg/v1/data/sql/CLAUDE.md`
- `data/docstore/` — see `pkg/v1/data/docstore/CLAUDE.md`
- `security/redact/` — see `pkg/v1/security/redact/CLAUDE.md`
- `app/statemachine/` — see `pkg/v1/app/statemachine/CLAUDE.md`
- `observe/profiling/` — see `pkg/v1/observe/profiling/CLAUDE.md`
- `data/codec/strictjson/` — see `pkg/v1/data/codec/strictjson/CLAUDE.md`
- `data/codec/strictjson/httpbody/` — see `pkg/v1/data/codec/strictjson/httpbody/CLAUDE.md`
- `data/codec/jsonshape/` — see `pkg/v1/data/codec/jsonshape/CLAUDE.md`
- `data/codec/jsonpatch/` — see `pkg/v1/data/codec/jsonpatch/CLAUDE.md`
- `data/codec/jcs/` — see `pkg/v1/data/codec/jcs/CLAUDE.md`
- `data/codec/jsonschema/` — see `pkg/v1/data/codec/jsonschema/CLAUDE.md`
- `data/codec/{asn1,baseenc,cbor,csv,flatbuffers,form,json,msgpack,multipart,ndjson,pem,tlv,toml,xml,yaml}/` — see their `CLAUDE.md`
- `data/codec/bson/` — see `pkg/v1/data/codec/bson/CLAUDE.md`
- `net/static/` — see `pkg/v1/net/static/CLAUDE.md`
- `observe/logger/writer/` — see `pkg/v1/observe/logger/writer/CLAUDE.md`
- `crypto/` — see `pkg/v1/crypto/CLAUDE.md`
- `crypto/agree/` — see `pkg/v1/crypto/agree/CLAUDE.md`
- `crypto/hash/` — see `pkg/v1/crypto/hash/CLAUDE.md`
- `crypto/kdf/` — see `pkg/v1/crypto/kdf/CLAUDE.md`
- `crypto/mac/` — see `pkg/v1/crypto/mac/CLAUDE.md`
- `crypto/password/` — see `pkg/v1/crypto/password/CLAUDE.md`
- `crypto/sign/` — see `pkg/v1/crypto/sign/CLAUDE.md`
- `security/` — see `pkg/v1/security/CLAUDE.md` (the security family — `authz`, `redact`, `secret`, `session`, `token` — and the rule that put them together; a directory with no Go code, so there is no `pkg/v1/security` to import)
- `net/` — see `pkg/v1/net/CLAUDE.md` (the net family — `server`, `client`, `tlsid`, `sse`, `websocket`, `static` — and the rule that put them together; a directory with no Go code, so there is no `pkg/v1/net` to import)
- `net/client/` — see `pkg/v1/net/client/CLAUDE.md`
- `net/tlsid/` — see `pkg/v1/net/tlsid/CLAUDE.md`
- `net/server/` — see `pkg/v1/net/server/CLAUDE.md`
- `net/sse/` — see `pkg/v1/net/sse/CLAUDE.md`
- `net/websocket/` — see `pkg/v1/net/websocket/CLAUDE.md`
- `proc/` — see `pkg/v1/proc/CLAUDE.md` (the capability preflight at the family's root, and its children — `process`, `signal`, `reaper`, `rlimit`, `cgroup`, `memlimit`, `systemd/notify`, `systemd/listen`, `ipc` — with the rule that put them together)
- `proc/process/` — see `pkg/v1/proc/process/CLAUDE.md`
- `proc/cgroup/` — see `pkg/v1/proc/cgroup/CLAUDE.md`
- `proc/memlimit/` — see `pkg/v1/proc/memlimit/CLAUDE.md`
- `proc/reaper/` — see `pkg/v1/proc/reaper/CLAUDE.md`
- `proc/rlimit/` — see `pkg/v1/proc/rlimit/CLAUDE.md`
- `proc/systemd/` — see `pkg/v1/proc/systemd/CLAUDE.md` (the two systemd protocols, `notify` and `listen`; a directory with no Go code, so there is no `pkg/v1/proc/systemd` to import)
- `proc/systemd/listen/` — see `pkg/v1/proc/systemd/listen/CLAUDE.md`
- `proc/systemd/notify/` — see `pkg/v1/proc/systemd/notify/CLAUDE.md`
- `proc/signal/` — see `pkg/v1/proc/signal/CLAUDE.md`
- `proc/ipc/` — see `pkg/v1/proc/ipc/CLAUDE.md`
- `proc/ipc/rpc/` — see `pkg/v1/proc/ipc/rpc/CLAUDE.md`
- `app/health/` — see `pkg/v1/app/health/CLAUDE.md`
- `app/lock/` — see `pkg/v1/app/lock/CLAUDE.md`

## Before v1 — the shapes this layer publishes

Every `type X = internal…Y` in this tree publishes `Y`'s **shape**, not just its
name. ADR 0039 covers the interface half (extend by a sibling, never by
widening); ADR 0040 covers this half, and gives it an expiry date — a concrete
shape may change while the module is v0, said out loud, and **not after**.

`docs/pre-v1-published-shape-audit.md` is the inventory that licence
applies to: **248 concrete structs** on the reorganised tree (125 when it was
first taken), split by what a change to each actually costs. 70 are
construction shapes a caller fills in by field name, where adding a field is
free. The expensive group — 124 — is the `*Value` shapes the SDK **returns**:
a caller destructures those, so even an added field breaks a composite literal
written without field names.

Review that list before tagging v1, not after. Regenerate it rather than trust
it: the count in it was wrong by a factor of 25 on the first pass, and looked
entirely plausible.
