# kitsunium/sdk

A Go SDK providing a normed, performant toolbox for downstream applications: structured logging, universal codec dispatch, typed errors with stable wire codes, a crypto suite, and OS process supervision.

## Packages

`pkg/v1` ships **54 packages**: 50 at the top level, plus four nested ones
(`logger/writer`, `logger/slogbridge`, `server/sse`, `server/websocket`). They
are grouped below by the job they do, and each links to its own generated
`README.md`.

### Observability

| Package | What it does |
|---|---|
| [`logger`](./pkg/v1/logger) + [`writer`](./pkg/v1/logger/writer), [`slogbridge`](./pkg/v1/logger/slogbridge) | Structured logger, one allocation per emit (see its BENCH.md). Multi-sink (console / file / syslog / memory), middleware chain, build-time version stamping; `writer` is the named, config-driven sink registry and `slogbridge` is the one package allowed to import `log/slog`, so a consumer facing a concrete `*slog.Logger` stops building a second pipeline. **Trace-correlated by default**: a record emitted inside a span carries `trace_id` / `span_id` as top-level fields; one emitted outside carries neither key rather than an invalid all-zero id. |
| [`metrics`](./pkg/v1/metrics) | The OpenTelemetry metrics **data model**, implemented from the specification with **zero** `go.opentelemetry.io` imports. Typed attributes whose kind is part of the series identity, explicit delta/cumulative temporality, OTLP/JSON on the wire. The Prometheus exporter is a deliberately lossy connector and says which losses it takes. |
| [`trace`](./pkg/v1/trace) | Distributed tracing on the OTel trace model + W3C Trace Context, both written from their documents. The sampling decision is taken once at the root and travels in the `sampled` bit, so a trace never has holes. A malformed `traceparent` starts a new trace and never fails a request. |
| [`health`](./pkg/v1/health) | Liveness, readiness and startup are three questions, so they take three **types**: a readiness `Check` gets a context and may reach a dependency; a liveness `SelfCheck` has none to reach one with. The classic mis-wiring does not compile by accident. |

### Application plumbing

| Package | What it does |
|---|---|
| [`config`](./pkg/v1/config) | env + file layering, typed decode, cross-OS poll-watch, and a **schema**: a missing required key fails at startup with every missing key named at once, an unknown key is refused by default, and a default is a layer *under* every source so `timeout = 0` stays 0. |
| [`lifecycle`](./pkg/v1/lifecycle) | Ordered bring-up, reverse teardown. A partial start is unwound before `Start` returns, through the same path an ordinary `Stop` uses; the shutdown budget is **per component**, so the first thing that will not finish cannot spend everyone else's. |
| [`cli`](./pkg/v1/cli) | Sub-commands, generated help and a typed exit status on the stdlib `flag`, with zero dependencies. `flag.ExitOnError` is refused by name — nothing here can end your process. |
| [`events`](./pkg/v1/events) | In-process **synchronous** bus keyed on the event's concrete Go type. Not a queue: no durability, no retry, no dead-letter path, and deliberately no async mode. |
| [`queue`](./pkg/v1/queue) | The asynchronous, durable counterpart. At-least-once, and the consequence is in the type: `Deliveries` counts from 1 and `HandlerIsIdempotent` is refused at its zero value. The durable broker's whole state is a directory and every transition is one `rename(2)`. |
| [`scheduler`](./pkg/v1/scheduler) | Five-field POSIX cron + fixed intervals. DST, missed deadlines and overlap are decided and documented rather than emergent. |
| [`cache`](./pkg/v1/cache) | The LRU+TTL primitive **and** the domain above it: invalidation by tag, stampede protection, L1/L2 chaining. Stampede protection stops at the process boundary, and says so. |
| [`clock`](./pkg/v1/clock) | The time port — `Clock` / `Waiter` / `Timed`, plus `System` and a `ManualClock` a test drives by hand. Every SDK `Clock` field takes one, so a timeout, a cron cadence or a lease expiry is asserted exactly, with no sleeping. |
| [`resilience`](./pkg/v1/resilience) | Retry, circuit breaker, rate limit, bulkhead, timeout, fallback, hedging — composable `Runner` policies. A policy's zero value is a safe default or an explicit refusal, never an inert policy. |
| [`lock`](./pkg/v1/lock) | Named exclusive leases over one process or one machine, with fencing tokens. What it does **not** guarantee is stated as loudly: the SDK can only issue a fence; where the resource cannot compare it, exclusion is not guaranteed against a GC pause. |
| [`id`](./pkg/v1/id) | UUIDv4/v7, ULID, snowflake, NanoID, KSUID, TypeID. |

### Network and web

| Package | What it does |
|---|---|
| [`server`](./pkg/v1/server) + [`sse`](./pkg/v1/server/sse), [`websocket`](./pkg/v1/server/websocket) | Inbound HTTP with TLS/mTLS identity, per-phase deadlines and policy; Server-Sent Events; and RFC 6455 WebSocket written in the stdlib, whose MUST-fails are enforced rather than tolerated. Draining is **announced** to the handler, never imposed. |
| [`client`](./pkg/v1/client) | The outbound half over the same substrate: policy, per-phase deadlines, call hooks. |
| [`tlsid`](./pkg/v1/tlsid) | TLS/mTLS identity from memory or disk, shared by both halves. |
| [`view`](./pkg/v1/view) | Server-side rendering **on** `html/template`, not a reimplementation — contextual escaping is an HTML parser, and a hand-written one is where the XSS would come from. `text/template` has no representation at all; an AST audit fails the build on the import. |
| [`i18n`](./pkg/v1/i18n) | Message translation with CLDR plurals over a **named** 13-language subset. An unsupported language is refused by name at construction, because falling back to English's two categories renders a wrong Polish sentence that nothing observes. |
| [`mail`](./pkg/v1/mail) | MIME composition + SMTP. A CR or LF in a header is **refused and never repaired**, because the three stdlib helpers that would repair it deliver a message you did not write while reporting success. |

### Data and security

| Package | What it does |
|---|---|
| [`codec`](./pkg/v1/codec) | Universal dispatch over a `Format` registry — 24 formats behind one `Marshal` / `Unmarshal` / `NewEncoder` / `NewDecoder`: `asn1-der`, `bson`, `cbor`, `csv`, `flatbuffers`, `form`, `json`, `msgpack`, `multipart`, `ndjson`, `pem`, `tlv`, `toml`, `xml`, `yaml` + 9 base-N encodings. |
| [`errs`](./pkg/v1/errs) | Typed errors with dotted-quad codes (`MM.LL.PP.SS`) + a wire-safe Public / log-only Private split. Construction (`New`, `Wrap`, `Field`) and introspection (`CodeOf`, `HasCode`, `NewPrefixMatcher`). |
| [`crypto`](./pkg/v1/crypto) + [`hash`](./pkg/v1/hash), [`sign`](./pkg/v1/sign), [`mac`](./pkg/v1/mac), [`kdf`](./pkg/v1/kdf), [`agree`](./pkg/v1/agree), [`password`](./pkg/v1/password) | AEAD seal/open with hidden nonces, hashing, signatures, MACs, key derivation, key agreement, password hashing — and JWK/JWKS, where a private export is opt-in and never the default. |
| [`token`](./pkg/v1/token) | JWT over JWS Compact + PASETO v4.public. The algorithm is bound by the constructor and never read from the token, so algorithm confusion is a call that does not compile; `alg:none` has no representation in the type. |
| [`secret`](./pkg/v1/secret) | A secret is a value no rendering writes down: `secret.Value` prints `<redacted>` under every fmt verb, JSON, text and slog, reveals its bytes only through `Reveal`, and decodes from a configuration string like any field. Versioned stores — memory, the environment with the Docker/Kubernetes `NAME_FILE` convention, a 0700 directory of atomically published records sealed with AES-256-GCM — a `Keyring` whose boxes name the version that sealed them so a rotation never breaks what came before it, and a `Rotator` that keeps at least two. |
| [`session`](./pkg/v1/session) | Server-side sessions. `Regenerate` is the only call that binds a subject and always mints a new identifier, so session fixation is prevented by the **absence** of any other spelling rather than by remembering a step. |
| [`authz`](./pkg/v1/authz) | RBAC + ABAC with no policy DSL — a condition is a Go func. **Abstention is a third verdict** and the zero value, because folding "no opinion" into a grant is a hole and into a refusal is an outage. |
| [`validation`](./pkg/v1/validation) | Constraints, located violations (`user.addresses[2].zip`), collect-all by default. A message names the rule and the bound but **never the value** — a security property with its own test. |
| [`sql`](./pkg/v1/sql) | Ports above `database/sql`, deliberately never an ORM, and **no driver**. A unit of work receives an `Executor` with no Commit and no Rollback, so a callee ending its caller's transaction is a sentence with no spelling. |
| [`vfs`](./pkg/v1/vfs) | Reading is `io/fs` **unchanged** (`FS` is a type alias, so `fs.WalkDir` applies with no adapter); writing is four verbs; publication is `rename(2)`-atomic — measured at 0 torn reads out of 600. |

### Process, platform and distribution

| Package | What it does |
|---|---|
| [`proc`](./pkg/v1/proc) + [`process`](./pkg/v1/process), [`signal`](./pkg/v1/signal), [`reaper`](./pkg/v1/reaper), [`rlimit`](./pkg/v1/rlimit), [`cgroup`](./pkg/v1/cgroup), [`sdnotify`](./pkg/v1/sdnotify), [`sdlisten`](./pkg/v1/sdlisten) | OS process supervision: spawn, signals, subreaping, resource limits, cgroups, systemd. Uniform typed `UnsupportedPlatform` where a kernel offers no native mechanism. |
| [`memlimit`](./pkg/v1/memlimit) | Reads the cgroup cap that already bounds **this** process — which the Go runtime does not — so a service in a 512 MiB container gets a soft limit instead of a SIGKILL. |
| [`git`](./pkg/v1/git) | What a branch changed, as a value that can say it does not know: "nothing changed" and "I could not tell" are opposite instructions, so the resolver degrades rather than answering with an empty set. |
| [`selfupdate`](./pkg/v1/selfupdate) | Signature **then** digest **then** disk. Comparing an archive against a checksum file fetched beside it verifies nothing; a build with no vendor key installs nothing. |
| [`entitlement`](./pkg/v1/entitlement) | Vendor-signed roster → grant, with an offline cache, an anti-rollback ratchet and a CI seat. Bring your own `Identity`: three methods, and none of them says "ssh". |
| [`gate`](./pkg/v1/gate) | May this invocation run? A policy value and a pure decision — it verifies nothing, upgrades nothing and never ends a process. |

## Install

```bash
go get github.com/kitsunium/sdk/pkg/v1/codec
go get github.com/kitsunium/sdk/pkg/v1/errs
go get github.com/kitsunium/sdk/pkg/v1/logger
```

## Quick example

```go
package main

import (
    "fmt"
    "github.com/kitsunium/sdk/pkg/v1/codec"
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

- **Layered architecture** — `kernel` (stdlib-only primitives) → `core` (interfaces + values) → `service` (implementations) → `pkg/v1` (stable public alias layer). Dependency direction enforced by Bazel `visibility` rules.
- **Typed errors throughout** — `fmt.Errorf` / `errors.New` are banned in production code. Every error carries a wire-safe `Public` message and a log-only `Private` envelope. AST audit gates the build.
- **One Marshal/Unmarshal for everything** — text, binary, base-encoded — `codec.Marshal(format, v)` works the same way regardless of the underlying wire shape.
- **The rules are checkable from outside** — the conventions this SDK states in its ADRs are enforced on *your* codebase too, by a tool the SDK ships:
  ```sh
  go run github.com/kitsunium/sdk/tools/sdkguard@latest -level=invariant ./...
  ```
  It catches the defects that never announce themselves — a second logging pipeline beside the SDK logger (two thresholds, two formats, half-stamped records), stdout used as a log destination under a stdio protocol, a `logger.Version` assigned at runtime. Rules target *constructs*, never imports, so `*slog.Logger` fields and `fmt.Fprintln(os.Stdout, …)` stay legitimate. It also warns — without failing your build — when your `go.mod` pins an SDK older than the latest release, which matters here because a patch is cut whenever an internal package changes, carrying fixes no public-API changelog would show. See ADR 0033 and `tools/sdkguard/CLAUDE.md`.
- **Multi-module workspace** — `internal/kernel`, `internal/core`, `internal/service`, `pkg`, root. Each can be built standalone with `GOWORK=off`; `go.work` is the umbrella over exactly those five. `e2e/`, `tools/genindex/` and `tools/sdkguard/` are auxiliary modules held deliberately outside it (adding them breaks Bazel's `go_deps`, which reads `go.work`) — build those with `GOWORK=off`.

## Releases

Versioning follows [semver](https://semver.org/) and Go's sub-directory tag convention: `pkg/<major>/v<MAJOR>.<MINOR>.<PATCH>`. Each major version (`pkg/v1`, future `pkg/v2`) has its own independent release cadence. Full release workflow lives in ADR 0007 (`docs/adr/`).

## Documentation

Full versioned documentation lives at the docs portal (run `make serve` from the repo root, or browse the published deployment). Highlights:

- **Concepts** — the 4-layer model, error semantics, codec dispatch
- **Changelog** — auto-generated from git log per release
- **Per-package reference** — `codec`, `errs`, `logger` with their full Go doc surfaces
- **ADRs** — architecture decisions, reachable from the "For contributors ↗" footer link

## Repository layout

```
internal/        stdlib-only primitives (kernel) + domain interfaces (core) + concrete impls (service)
pkg/v1/          stable public API — type aliases + ergonomic helpers
docs/            ADRs + the Astro-based docs site
scripts/release/ release tooling — see ADR 0007
tools/           build-time helpers (workspace_status, genindex, alloc-lane-targets)
                 + sdkguard/ — consumer-facing rule enforcement (ADR 0033)
e2e/             real-kernel conformance harness (auxiliary module, GOWORK=off)
```

## Verification

```bash
make build       # bazel mod tidy + gazelle + gofumpt + bazel build //...
make test        # every *_test target green incl. AST audits
make test-alloc  # race-off allocation gates (the only lane running //go:build !race tests)
make lint        # drift check (read-only): mod tidy + gazelle + gofumpt + ktn-linter + alloc-lane coverage + guard
make guard       # sdkguard over the SDK's own tree at invariant level (ADR 0033)
make bench       # regenerate codec BENCH.md from real Go benchmarks
```

## Benchmarks

Kernel hot-path benchmarks live next to their source as
`internal/kernel/*/*_bench_test.go`. Run them via:

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
