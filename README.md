# kitsunium/sdk

A Go SDK providing a normed, performant toolbox for downstream applications: structured logging, universal codec dispatch, typed errors with stable wire codes, a crypto suite, and OS process supervision.

## Packages

`pkg/v1` ships **87 packages**: 4 at the top level — `errs`, `clock`,
`crypto` and `proc` — plus eighty-three nested ones (the six scheme facades under
`crypto/` — `agree`, `hash`, `kdf`, `mac`, `password`, `sign` — the six under
`concur/` — `group`, `singleflight`, `worker`, `batcher`, `snapshot`,
`recycler` — the two under `collections/` — `heap`, `ring` — the five under
`security/` — `authz`, `redact`, `secret`, `session`, `token` — the six under
`net/` — `server`, `client`, `tlsid`, `sse`, `websocket`, `static` — the six
under `observe/` — `logger`, `logger/writer`, `logger/slogbridge`, `metrics`,
`trace`, `profiling` — the nine under `proc/` — `process`, `signal`, `reaper`,
`rlimit`, `cgroup`, `memlimit`, `ipc`, `systemd/notify`, `systemd/listen` —
the twenty-eight under `data/` — `codec`, its sixteen per-format packages
(`codec/asn1`, `codec/baseenc`, `codec/bson`, `codec/cbor`, `codec/csv`,
`codec/flatbuffers`, `codec/form`, `codec/json`, `codec/msgpack`,
`codec/multipart`, `codec/ndjson`, `codec/pem`, `codec/tlv`, `codec/toml`,
`codec/xml`, `codec/yaml`), `codec/strictjson`, `codec/strictjson/httpbody`,
`codec/jsonshape`, `codec/jsonpatch`, `transform`, `sql`, `docstore`,
`queue`, `cache`, `vfs`, `semver` — and the fifteen under `app/` — `config`,
`cli`, `i18n`, `validation`, `view`, `events`, `scheduler`, `statemachine`,
`resilience`, `lifecycle`, `health`, `lock`, `id`, `mail`, `mail/spool`),
and links the standard library and nothing else (ADR 0156). They are grouped
below by the job they do, and each links to its own generated `README.md`. The
distribution mechanisms that close the list are the framework's packages,
imported from `github.com/kitsunium/sdk/framework/…` (ADR 0158).

### Observability

| Package | What it does |
|---|---|
| [`logger`](./pkg/v1/observe/logger) + [`writer`](./pkg/v1/observe/logger/writer), [`slogbridge`](./pkg/v1/observe/logger/slogbridge) | Structured logger, one allocation per emit (see its BENCH.md). Multi-sink (console / file / syslog / memory), middleware chain, build-time version stamping; `writer` is the named, config-driven sink registry and `slogbridge` is the one package allowed to import `log/slog`, so a consumer facing a concrete `*slog.Logger` stops building a second pipeline. **Trace-correlated by default**: a record emitted inside a span carries `trace_id` / `span_id` as top-level fields; one emitted outside carries neither key rather than an invalid all-zero id. `LevelGate` gives one branch of a fan-out a floor of its own — a terminal at Info, a viewer at Debug. |
| [`metrics`](./pkg/v1/observe/metrics) | The OpenTelemetry metrics **data model**, implemented from the specification with **zero** `go.opentelemetry.io` imports. Typed attributes whose kind is part of the series identity, explicit delta/cumulative temporality, OTLP/JSON on the wire. The Prometheus exporter is a deliberately lossy connector and says which losses it takes. |
| [`trace`](./pkg/v1/observe/trace) | Distributed tracing on the OTel trace model + W3C Trace Context, both written from their documents. The sampling decision is taken once at the root and travels in the `sampled` bit, so a trace never has holes. A malformed `traceparent` starts a new trace and never fails a request. |
| [`health`](./pkg/v1/app/health) | Liveness, readiness and startup are three questions, so they take three **types**: a readiness `Check` gets a context and may reach a dependency; a liveness `SelfCheck` has none to reach one with. The classic mis-wiring does not compile by accident. `Ask` is the other end — the question a container's HEALTHCHECK asks, from a binary in an image with no shell and no curl: ready means 200 and nothing else, and every other outcome has its own code. |
| [`profiling`](./pkg/v1/observe/profiling) | The process's own CPU, heap and goroutines. A CPU window and the live heap captured through `runtime/pprof` — one CPU profiler per process, so a second capture is refused rather than queued — the pprof format decoded with the standard library, samples folded onto owners **you** name so the parts add up to the total exactly, and the runtime's goroutine dump read into goroutines and grouped. A heap profile is sampled, and the doc says so first. |

### Application plumbing

| Package | What it does |
|---|---|
| [`config`](./pkg/v1/app/config) | env + file layering, typed decode, cross-OS poll-watch, and a **schema**: a missing required key fails at startup with every missing key named at once, an unknown key is refused by default, and a default is a layer *under* every source so `timeout = 0` stays 0. |
| [`lifecycle`](./pkg/v1/app/lifecycle) | Ordered bring-up, reverse teardown. A partial start is unwound before `Start` returns, through the same path an ordinary `Stop` uses; the shutdown budget is **per component**, so the first thing that will not finish cannot spend everyone else's. A **supervisor** keeps a component's loop running: restarted after an error, an early return or a panic, on a growing backoff, and joined on stop. |
| [`cli`](./pkg/v1/app/cli) | Sub-commands, generated help and a typed exit status on the stdlib `flag`, with zero dependencies. `flag.ExitOnError` is refused by name — nothing here can end your process. |
| [`events`](./pkg/v1/app/events) | In-process **synchronous** bus keyed on the event's concrete Go type. Not a queue: no durability, no retry, no dead-letter path, and deliberately no async mode. |
| [`queue`](./pkg/v1/data/queue) | The asynchronous, durable counterpart. At-least-once, and the consequence is in the type: `Deliveries` counts from 1 and `HandlerIsIdempotent` is refused at its zero value. The durable broker's whole state is a directory and every transition is one `rename(2)`. An idle consumer sleeps until a publication, a retry falling due or a lapsed lease wakes it, so the poll only bounds what another process publishes. |
| [`scheduler`](./pkg/v1/app/scheduler) | Five-field POSIX cron + fixed intervals. DST, missed deadlines and overlap are decided and documented rather than emergent. |
| [`statemachine`](./pkg/v1/app/statemachine) | Entities of **your** store moved along declared states: events you fire, timers after a duration in a state, deadlines the entity carries, guards on it. One transition per entity at a time, a hook that panics never leaves one locked, and a replace never resurrects a deleted entity. The loop keeps an agenda — one heap entry per entity — so it sleeps until the next transition due or a write and finds it in O(log N), not by re-reading the store. |
| [`cache`](./pkg/v1/data/cache) | The LRU+TTL primitive **and** the domain above it: invalidation by tag, stampede protection, L1/L2 chaining. Stampede protection stops at the process boundary, and says so. |
| [`clock`](./pkg/v1/clock) | The time port — `Clock` / `Waiter` / `Timed`, plus `System` and a `ManualClock` a test drives by hand. Every SDK `Clock` field takes one, so a timeout, a cron cadence or a lease expiry is asserted exactly, with no sleeping. |
| [`semver`](./pkg/v1/data/semver) | Version precedence the way Go writes versions — SemVer 2.0.0 with a leading `v` — and Go pseudo-versions recognised and read, with `golang.org/x/mod`'s names and answers and nothing outside the standard library. An unreadable string sorts first, numbers of any size order exactly, nothing allocates. |
| [`group`](./pkg/v1/concur/group), [`singleflight`](./pkg/v1/concur/singleflight), [`worker`](./pkg/v1/concur/worker), [`batcher`](./pkg/v1/concur/batcher), [`snapshot`](./pkg/v1/concur/snapshot), [`recycler`](./pkg/v1/concur/recycler) (`concur/`) + [`heap`](./pkg/v1/collections/heap), [`ring`](./pkg/v1/collections/ring) (`collections/`) | The kernel's concurrency primitives and containers, published as pure aliases (ADR 0159): a fan-out that joins every failure or collects every value, one execution per key for concurrent callers, a loop ticking on an injected clock, a size-or-time batcher, a copy-on-write value, a capped object pool, a binary heap and a single-producer ring. |
| [`resilience`](./pkg/v1/app/resilience) | Retry, circuit breaker, rate limit — one bucket, or one per caller with the set of callers bounded — bulkhead, timeout, fallback, hedging: composable `Runner` policies. The backoff curve is public and never wraps negative, and the retry waits on a clock a test can move. A policy's zero value is a safe default or an explicit refusal, never an inert policy. |
| [`lock`](./pkg/v1/app/lock) | Named exclusive leases over one process or one machine, with fencing tokens. What it does **not** guarantee is stated as loudly: the SDK can only issue a fence; where the resource cannot compare it, exclusion is not guaranteed against a GC pause. |
| [`id`](./pkg/v1/app/id) | UUIDv4/v7, ULID, snowflake, NanoID, KSUID, TypeID. |

### Network and web

| Package | What it does |
|---|---|
| [`server`](./pkg/v1/net/server) + [`sse`](./pkg/v1/net/sse), [`websocket`](./pkg/v1/net/websocket), [`static`](./pkg/v1/net/static) | Inbound HTTP with TLS/mTLS identity, per-phase deadlines and policy; Server-Sent Events; and RFC 6455 WebSocket written in the stdlib, whose MUST-fails are enforced rather than tolerated. `static` serves a file tree — an embedded single-page application — the way `http.FileServerFS` does not: never a directory listing, never HTML for a missing script, and the security headers on every answer. Draining is **announced** to the handler, never imposed. |
| [`client`](./pkg/v1/net/client) | The outbound half over the same substrate: policy, per-phase deadlines, call hooks. |
| [`tlsid`](./pkg/v1/net/tlsid) | TLS/mTLS identity from memory or disk, shared by both halves. |
| [`view`](./pkg/v1/app/view) | Server-side rendering **on** `html/template`, not a reimplementation — contextual escaping is an HTML parser, and a hand-written one is where the XSS would come from. `text/template` has no representation at all; an AST audit fails the build on the import. |
| [`i18n`](./pkg/v1/app/i18n) | Message translation with CLDR plurals over a **named** 13-language subset. An unsupported language is refused by name at construction, because falling back to English's two categories renders a wrong Polish sentence that nothing observes. |
| [`mail`](./pkg/v1/app/mail) + [`spool`](./pkg/v1/app/mail/spool) | MIME composition + SMTP. A CR or LF in a header is **refused and never repaired**, because the three stdlib helpers that would repair it deliver a message you did not write while reporting success. And a durable outbox, the **spool** (its own package): a mail is validated at `Send` and queued, retried on a growing backoff, dead-lettered with its last failure. A redelivery of a mail it delivered is dropped; the one resend left, after a crash between the relay's acceptance and the acknowledgement, carries the same Message-ID, so a receiver can recognise it. |

### Data and security

| Package | What it does |
|---|---|
| [`codec`](./pkg/v1/data/codec) + one package per format, [`json`](./pkg/v1/data/codec/json) … [`xml`](./pkg/v1/data/codec/xml), and [`strictjson`](./pkg/v1/data/codec/strictjson) (+ [`httpbody`](./pkg/v1/data/codec/strictjson/httpbody)), [`jsonshape`](./pkg/v1/data/codec/jsonshape), [`jsonpatch`](./pkg/v1/data/codec/jsonpatch), [`json`](./pkg/v1/data/codec/json) / [`yaml`](./pkg/v1/data/codec/yaml) / [`toml`](./pkg/v1/data/codec/toml) / [`bson`](./pkg/v1/data/codec/bson) | Universal dispatch over a `Format` registry — 24 formats behind one `Marshal` / `Unmarshal` / `NewEncoder` / `NewDecoder`: `asn1-der`, `bson`, `cbor`, `csv`, `flatbuffers`, `form`, `json`, `msgpack`, `multipart`, `ndjson`, `pem`, `tlv`, `toml`, `xml`, `yaml` + 9 base-N encodings. `strictjson` is the other JSON decoder, for documents somebody else wrote: one reading or a refusal — no duplicate name, no case-only match, no unknown member, no trailing data — within a byte bound, and no refusal ever quotes the input. `jsonshape` describes a Go type's wire shape under encoding/json — members resolved exactly as the encoder resolves them, each with the Go field behind it. `jsonpatch` says what changed between two JSON documents, as RFC 6902 operations with the value each writes and the value it replaces. Each per-format package — [`asn1`](./pkg/v1/data/codec/asn1), [`baseenc`](./pkg/v1/data/codec/baseenc) (nine base-N formats), [`bson`](./pkg/v1/data/codec/bson), [`cbor`](./pkg/v1/data/codec/cbor), [`csv`](./pkg/v1/data/codec/csv), [`flatbuffers`](./pkg/v1/data/codec/flatbuffers), [`form`](./pkg/v1/data/codec/form), [`json`](./pkg/v1/data/codec/json), [`msgpack`](./pkg/v1/data/codec/msgpack), [`multipart`](./pkg/v1/data/codec/multipart), [`ndjson`](./pkg/v1/data/codec/ndjson), [`pem`](./pkg/v1/data/codec/pem), [`tlv`](./pkg/v1/data/codec/tlv), [`toml`](./pkg/v1/data/codec/toml), [`xml`](./pkg/v1/data/codec/xml), [`yaml`](./pkg/v1/data/codec/yaml) — registers its format and names its codes, so a program reading YAML links the SDK's own YAML reader and no other codec, and `codec` is their aggregate; `bson` also names BSON's value types and has its own `Marshal` / `Unmarshal`. A request body is `strictjson/httpbody`'s, so the decoder itself links no `net/http`. Every codec is implemented on the standard library alone — YAML as a named subset of YAML 1.2.2 that refuses anchors, tags, merge keys and the other constructs it leaves out by name; the full `yaml.v3` reader is the opt-in module `third-party/codec/yaml`, registered as `yaml-full`. |
| [`transform`](./pkg/v1/data/transform) | Compression without the codec package: gzip, raw DEFLATE and the zlib envelope from the standard library, zstd and s2 from the opt-in `third-party/transform`. `DecompressBounded` takes your ceiling and refuses a stream past it rather than cutting it short; a scheme that can be told the ceiling stops at it, so it bounds the work and not only the verdict. |
| [`errs`](./pkg/v1/errs) | Typed errors with dotted-quad codes (`MM.LL.PP.SS`) + a wire-safe Public / log-only Private split. Construction (`New`, `Wrap`, `Field`) and introspection (`CodeOf`, `HasCode`, `NewPrefixMatcher`). |
| [`crypto`](./pkg/v1/crypto) + [`hash`](./pkg/v1/crypto/hash), [`sign`](./pkg/v1/crypto/sign), [`mac`](./pkg/v1/crypto/mac), [`kdf`](./pkg/v1/crypto/kdf), [`agree`](./pkg/v1/crypto/agree), [`password`](./pkg/v1/crypto/password) | AEAD seal/open with hidden nonces, hashing, signatures, MACs, key derivation, key agreement, password hashing and a check against the ten thousand most common passwords — and JWK/JWKS, where a private export is opt-in and never the default. |
| [`token`](./pkg/v1/security/token) | JWT over JWS Compact + PASETO v4.public. The algorithm is bound by the constructor and never read from the token, so algorithm confusion is a call that does not compile; `alg:none` has no representation in the type. |
| [`secret`](./pkg/v1/security/secret) | A secret is a value no rendering writes down: `secret.Value` prints `<redacted>` under every fmt verb, JSON, text and slog, reveals its bytes only through `Reveal`, and decodes from a configuration string like any field. Versioned stores — memory, the environment with the Docker/Kubernetes `NAME_FILE` convention, a 0700 directory of atomically published records sealed with AES-256-GCM — a `Keyring` whose boxes name the version that sealed them so a rotation never breaks what came before it, and a `Rotator` that keeps at least two. `SubjectKeys`: one data key per subject under that rotating root, so a rotation re-wraps one small key per subject and never a field, and destroying a subject's key erases every copy it sealed. |
| [`session`](./pkg/v1/security/session) | Server-side sessions. `Regenerate` is the only call that binds a subject and always mints a new identifier, so session fixation is prevented by the **absence** of any other spelling rather than by remembering a step. |
| [`authz`](./pkg/v1/security/authz) | RBAC + ABAC with no policy DSL — a condition is a Go func. **Abstention is a third verdict** and the zero value, because folding "no opinion" into a grant is a hole and into a refusal is an outage. |
| [`validation`](./pkg/v1/app/validation) | Constraints, located violations (`user.addresses[2].zip`), collect-all by default. A message names the rule and the bound but **never the value** — a security property with its own test. |
| [`sql`](./pkg/v1/data/sql) | Ports above `database/sql`, deliberately never an ORM, and **no driver**. A unit of work receives an `Executor` with no Commit and no Rollback, so a callee ending its caller's transaction is a sentence with no spelling. |
| [`vfs`](./pkg/v1/data/vfs) | Reading is `io/fs` **unchanged** (`FS` is a type alias, so `fs.WalkDir` applies with no adapter); writing is four verbs; publication is `rename(2)`-atomic — measured at 0 torn reads out of 600. |
| [`docstore`](./pkg/v1/data/docstore) | Typed, keyed JSON documents with unique and multi-valued indexes, in memory, on a filesystem or over SQL. A write is durable before it returns and costs the same at a hundred documents or a hundred thousand: it publishes one small file, and the store rests as ONE readable snapshot. Asked to, it keeps each document's last **versions** in the document's own write, so no crash ever separates them. |
| [`redact`](./pkg/v1/security/redact) | Values, JSON documents, text and log attributes shown with their secrets replaced — a name that says it is one, a field declared secret, a URL's credentials — within an **exact** byte bound, never touching the input. What it does not recognise is stated, because it is a display filter and not an access control. |

### Process, platform and distribution

| Package | What it does |
|---|---|
| [`proc`](./pkg/v1/proc) + [`process`](./pkg/v1/proc/process), [`signal`](./pkg/v1/proc/signal), [`reaper`](./pkg/v1/proc/reaper), [`rlimit`](./pkg/v1/proc/rlimit), [`cgroup`](./pkg/v1/proc/cgroup), [`systemd/notify`](./pkg/v1/proc/systemd/notify), [`systemd/listen`](./pkg/v1/proc/systemd/listen) | OS process supervision: spawn, signals, subreaping, resource limits, cgroups, systemd readiness and socket activation. Uniform typed `UnsupportedPlatform` where a kernel offers no native mechanism. `process` also reads the process itself: its runtime state and the build it came from, a module's release, commit and local directory kept apart. |
| [`memlimit`](./pkg/v1/proc/memlimit) | Reads the cgroup cap that already bounds **this** process — which the Go runtime does not — so a service in a 512 MiB container gets a soft limit instead of a SIGKILL. |
| [`ipc`](./pkg/v1/proc/ipc) | A private socket between processes of one machine: its directory gates it and the path above that directory is refused where another account could steer it; the kernel names the peer where it can (`SO_PEERCRED` on Linux; on Windows a named pipe whose own DACL names the account, each end's account read from the other's token). |
| [`git`](./framework/git) (framework) | What a branch changed, as a value that can say it does not know: "nothing changed" and "I could not tell" are opposite instructions, so the resolver degrades rather than answering with an empty set. And what a working tree is at — its commit, that commit's time, whether a tracked file differs — through the same hardened invocations. |
| [`selfupdate`](./framework/selfupdate) (framework) | Signature **then** digest **then** disk. Comparing an archive against a checksum file fetched beside it verifies nothing; a build with no vendor key installs nothing. |
| [`entitlement`](./framework/entitlement) (framework) | Vendor-signed roster → grant, with an offline cache, an anti-rollback ratchet and a CI seat. Bring your own `Identity`: three methods, and none of them says "ssh". |
| [`gate`](./framework/gate) (framework) | May this invocation run? A policy value and a pure decision — it verifies nothing, upgrades nothing and never ends a process. |

## Install

The SDK is one Go module, `github.com/kitsunium/sdk` — `pkg/v1`, the framework
and the internals they are built on — and it links the standard library and
nothing else (ADR 0156, ADR 0162):

```bash
go get github.com/kitsunium/sdk@latest
```

then import the packages you use — `github.com/kitsunium/sdk/pkg/v1/data/codec`,
`…/pkg/v1/errs`, `…/framework/kit`. Require the module BEFORE a `go mod tidy`:
Go resolves an import nothing in go.mod provides to the module with the longest
path that provides it, and until you require `github.com/kitsunium/sdk` that is
the retired `…/pkg` at v0.17.0. An integration that needs a vendor library
is a module of its own, required by name, so you take that vendor and no
other: `go get github.com/kitsunium/sdk/third-party/aws@latest`, or
`…/framework/connectors/postgres`.

### Migrating from `…/pkg` and `…/framework` (v0.17.0 and before)

Up to v0.17.0 the SDK was split into modules — `…/pkg`, `…/framework` and
three `…/internal/*` ones your go.mod lists as `// indirect`. Since v0.18.0 it
is one; the import paths did not change, the requirement does. One command
moves a go.mod over, and it must drop every old module, or each package is
found in two modules of the build and it fails with an ambiguous import:

```bash
go get github.com/kitsunium/sdk@v0.18.0 \
  github.com/kitsunium/sdk/pkg@none \
  github.com/kitsunium/sdk/framework@none \
  github.com/kitsunium/sdk/internal/kernel@none \
  github.com/kitsunium/sdk/internal/core@none \
  github.com/kitsunium/sdk/internal/service@none
go mod tidy
```

Add `github.com/kitsunium/sdk/<module>@v0.18.0` to the same `go get` for each
vendor or connector module your go.mod requires (`third-party/aws`,
`framework/connectors/postgres`…). `sdkguard` prints this command when it finds
a go.mod still on `…/pkg` (ADR 0162).

## Quick example

```go
package main

import (
    "fmt"
    "github.com/kitsunium/sdk/pkg/v1/data/codec"
)

func main() {
    payload := map[string]any{"hello": "world"}
    out, err := codec.Marshal(codec.JSON, payload)
    if err != nil {
        panic(err)
    }
    fmt.Println(string(out))
}
```

## What makes this SDK different

- **Layered architecture** — `kernel` (stdlib-only primitives) → `core` (interfaces + values + error codes) → `service` (implementations) → `pkg/v1` (stable public alias layer) → `framework`. Each layer is grouped by the same families — `crypto`, `security`, `net`, `proc`, `observe`, `data`, `app` (ADR 0155). Dependency direction enforced on the build graph by `scripts/check-layer-deps.sh` (ADR 0068).
- **Typed errors throughout** — `fmt.Errorf` / `errors.New` are banned in production code. Every error carries a wire-safe `Public` message and a log-only `Private` envelope. `make guard` gates it: the SDK runs its own `sdkguard` rule SDK002 over its production tree in `make lint` and CI. And every code is declared in one place — the core package at the path of the engine that raises it, so a domain's codes are found together and its public sentinels all alias the core (ADR 0160, gated by `scripts/pre-commit/check-core-symmetry.sh`).
- **One Marshal/Unmarshal for everything** — text, binary, base-encoded — `codec.Marshal(format, v)` works the same way regardless of the underlying wire shape.
- **The rules are checkable from outside** — the conventions this SDK states in its ADRs are enforced on *your* codebase too, by a tool the SDK ships:
  ```sh
  go run github.com/kitsunium/sdk/tools/sdkguard@latest -level=invariant ./...
  ```
  It catches the defects that never announce themselves — a second logging pipeline beside the SDK logger (two thresholds, two formats, half-stamped records), stdout used as a log destination under a stdio protocol, a `logger.Version` assigned at runtime. Rules target *constructs*, never imports, so `*slog.Logger` fields and `fmt.Fprintln(os.Stdout, …)` stay legitimate. It also warns — without failing your build — when your `go.mod` pins an SDK older than the latest release, which matters here because a patch is cut whenever an internal package changes, carrying fixes no public-API changelog would show. See ADR 0033 and `tools/sdkguard/CLAUDE.md`.
- **One module, and a module per vendor** — the SDK is one module at the root (ADR 0162); `go.work` adds the thirteen modules that require a vendor library: the framework's four connectors (three database engines and entitlement's ssh identity) and nine vendor modules under `third-party/` — one per vendor, `codec/yaml` included (ADR 0157). Each can be built standalone with `GOWORK=off`, and `bash scripts/ci/go-modules.sh` lists them all. `e2e/`, `tools/genindex/` and `tools/sdkguard/` are auxiliary modules held deliberately outside it (adding them breaks Bazel's `go_deps`, which reads `go.work`) — build those with `GOWORK=off`.

## Releases

Versioning follows [semver](https://semver.org/) and Go's sub-directory tag convention. A release is ONE tag, `vX.Y.Z`, on the SDK module `github.com/kitsunium/sdk`, and one GitHub release; a vendor or connector module is tagged at the same version — `third-party/<path>/vX.Y.Z`, `framework/connectors/<engine>/vX.Y.Z` — only by a release that changes it, and the release's notes list the ones it cut (ADR 0162). The releases up to v0.17.0 were cut as `pkg/vX.Y.Z` with every other module in lockstep (ADR 0017, ADR 0147, ADR 0157); those tags stay. A release is sized by the `release:*` label a maintainer sets on the pull request (ADR 0135). The full release workflow lives in ADR 0007 and the records that amend it (`docs/adr/`).

## Documentation

Full versioned documentation lives at the docs portal (run `make serve` from the repo root, or browse the published deployment). Highlights:

- **Concepts** — the 4-layer model, error semantics, codec dispatch
- **Changelog** — auto-generated from git log per release
- **Per-package reference** — `codec`, `errs`, `logger` with their full Go doc surfaces
- **ADRs** — architecture decisions, reachable from the "For contributors ↗" footer link

## Repository layout

```
internal/        stdlib-only primitives (kernel) + domain interfaces, values and codes (core)
                 + concrete impls (service), grouped by family in each layer (ADR 0155)
pkg/v1/          stable public API — type aliases + ergonomic helpers
docs/            ADRs + the Astro-based docs site
scripts/release/ release tooling — see ADR 0007
tools/           build-time helpers (workspace_status, genindex, alloc-lane-targets)
                 + sdkguard/ — consumer-facing rule enforcement (ADR 0033)
framework/       the layer above pkg/v1 a product imports (ADR 0147), the
                 distribution mechanisms and their ssh connector included (ADR 0158)
third-party/     opt-in vendor integrations, one Go module per vendor (ADR 0157)
e2e/             real-kernel conformance harness + the Docker-backed integration
                 suites (auxiliary module, GOWORK=off)
```

## Verification

```bash
make build       # bazel mod tidy + gazelle + gofumpt + bazel build //...
make test        # every *_test target green incl. AST audits
make test-alloc  # race-off allocation gates (the only lane running //go:build !race tests)
make lint        # drift check (read-only): mod tidy + gazelle + gofumpt + guard + doc links + ktn-linter + the guards
                 #   (alloc-lane and audit coverage, domain docs, core symmetry, package docs, BENCH.md, error codes) + layer firewall
make guard       # sdkguard over the SDK's own tree: the invariants (ADR 0033), and SDK002 over production code (rule 2)
make bench       # regenerate codec BENCH.md from real Go benchmarks
```

## Benchmarks

Kernel hot-path benchmarks live next to their source as
`internal/kernel/**/*_bench_test.go`. Run them via:

```bash
make sdk-bench          # steady-state numbers → .bench.out (benchstat-friendly)
make sdk-bench-profile  # CPU / mem / block / mutex profiles → profiles/
make sdk-bench-compare  # benchstat A/B comparison vs a recorded .bench.main.out
```

See `docs/BENCHMARK-TEMPLATE.md` for the bench conventions (white-box package,
`b.Loop()`, mandatory `b.ReportAllocs()`, `_Parallel` variants, and the
allocation budgets the CI gate enforces).

Those budgets are checked by the race-off allocation lane — `make test-alloc`,
whose target list lives in `tools/alloc-lane-targets.txt`. A test carrying
`//go:build !race` is invisible to the race suite, so that lane is its only
gate; `scripts/pre-commit/check-alloc-lane-coverage.sh` fails the build if such
a test exists in a package the lane does not cover.

## License

MIT — see [LICENSE](./LICENSE) for the full text.
