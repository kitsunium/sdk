<!-- updated: 2026-05-21T21:27:56Z -->
# kitsunium/sdk

## Purpose

Go SDK providing a normed, performant toolbox for downstream applications. Ten domains ship today — a structured **logger** (one alloc per emit, multi-sink; the `sync.Pool` recycles the builder but the handler clones the attrs — see `pkg/v1/logger/BENCH.md`, pinned by `TestV116BuildSendAllocatesOnePerEmit`), a universal **codec** (24 wire formats behind a single `Marshal/Unmarshal` dispatch), typed **errs** (dotted-quad codes + public/private split), a **crypto** suite (AEAD, hash, sign, MAC, KDF, key-agreement, password hashing, and JWK/JWKS key representation — RFC 7517 for EC/OKP/oct, with private export opt-in and never the default), **transform** (compression — stdlib gzip/flate/zlib; `flate` is raw DEFLATE, `zlib` is the RFC 1950 envelope HTTP misnames `deflate`), OS **proc** supervision, **id** generation (UUIDv4/v7, ULID, snowflake, NanoID, KSUID, TypeID — ADR 0024), **resilience** policies (retry/circuit-breaker/rate-limit/bulkhead/timeout/fallback/hedging — ADR 0026; hedging duplicates the operation, so it demands an in-code idempotence assertion and a load cap — ADR 0031), **metrics** (counter/gauge/histogram + exporter registry + labelled series with a bounded, visibly-overflowing cardinality — ADR 0027; the stdlib `prometheus` exporter renders the text exposition format and REFUSES a name that format cannot spell rather than transliterating it into a collision), and **config** (env+file layering, typed decode, cross-OS poll-watch — ADR 0028). The Phase-B wave also adds the kernel `cache` primitive (ADR 0025). New domains land in the same 4-layer shape (ADR 0001).

**Repository**: `github.com/kitsunium/sdk` · **Module name**: same · **Go**: 1.27.0 (pinned in `MODULE.bazel`)

## Architecture at a glance

```
internal/
├── kernel/        stdlib-only AND generic primitives
│                  batcher, buffer, cache, clock, errs, recycler,
│                  ring, snapshot, worker
├── core/          domain interfaces + domain values
│                  codec (+ scratch), crypto, id, logger, logger/level,
│                  proc, transform, writer
└── service/       concrete implementations
                   logger (+ encoder, sink/{console,file,memory,syslog},
                             middleware/{async,encwrite,failover,multi,
                                         recover,route,sample,tee})
                   writer (console, dbsink, file, journald, levelgate,
                           nettransport, rotfile)
                   crypto (aesgcm, ecdsasig, ed25519sig, hkdfsha256,
                           hmacsha2, jwk, keyenvelope, keytree, pbkdf2pw,
                           stdhash, streamaead, x25519)
                   codec  (asn1, baseenc, bson, cbor, csv, flatbuffers,
                           form, json, msgpack, multipart, ndjson, pem, tlv, toml,
                           xml, yaml)
                   proc   (cgroup, exec, reaper, rlimit, sdlisten,
                           sdnotify, signal)
                   id     (uuidv4, uuidv7, ulid, snowflake, nanoid,
                           ksuid, typeid)
                   transform
pkg/
└── v1/            stable public API (type aliases + ergonomic helpers)
    ├── logger/    (+ ldflags-injected Version, + writer/, + slogbridge/)
    ├── errs/      (construction + introspection: New, Wrap, CodeOf, …)
    ├── codec/     (blank-imports all 16 service codecs + transform)
    ├── crypto/    (+ agree, hash, kdf, mac, password, sign)
    ├── id/        (UUIDv4/v7, ULID, snowflake, NanoID, KSUID, TypeID — ADR 0024)
    └── proc/      (+ cgroup, process, reaper, rlimit, sdlisten,
                      sdnotify, signal)
third-party/       opt-in vendor integrations (root module only)
                   aws/writer/{cloudwatch,s3}, codec/{hcl,protobuf},
                   db/writer/{clickhouse,mysql,redis},
                   x-crypto/{argon2id,xchacha}
```

`baseenc` is NOT a `pkg/v1/codec` subpackage — it is a service codec
(`internal/service/codec/baseenc`) registering nine base-N Formats
(base16/32/45/58/62/64/64url, hex, ascii85) through the same registry as
every other codec.

- Five SDK modules held together by `go.work` — plus two **auxiliary** modules deliberately kept OUT of it: `e2e/` and `tools/genindex/`. Bazel's `go_deps` extension reads `go.work` and cannot process extra modules, so adding either breaks the build; both carry `replace` directives and are built with `GOWORK=off`. Counting `go.mod` files therefore yields seven — that is not drift, it is the invariant. See `e2e/CLAUDE.md` §Do NOT and `tools/CLAUDE.md` §Do NOT. The five workspace modules are: root (umbrella — also hosts the opt-in, vendor-dependent integrations under `third-party/*`, e.g. the AWS writers; see ADR 0012), `internal/kernel`, `internal/core`, `internal/service`, and the public `pkg` (module `github.com/kitsunium/sdk/pkg`, `go.mod` at `pkg/go.mod`; its consumer packages live under `pkg/v1/` and import as `…/pkg/v1/*`, but the *module* is the bare `…/pkg` because Go forbids a `/v1` module-path suffix — ADR 0017). Each module-local `go.mod` carries `replace` directives so `GOWORK=off go build ./...` per-module still works. Heavy vendor deps (AWS SDK) live in the **root** `go.mod` only — nothing requires the root module, so `pkg` consumers stay dep-light.
- Dependency direction is strictly top-down: kernel → core → service → pkg/v1. Enforced by Bazel `package_group` + `visibility` (see ADR 0004). A rogue import fails `bazel build` before it ever reaches the linter.
- Consumers import only `pkg/v1/*`; `internal/*` is blocked by Go's `internal/` firewall AND by the Bazel layer visibility.
- Build / test / lint go through **Bazel 9** — see ADR 0004. `go test ./...` still works locally for quick iteration but CI only runs `bazel`. Because the two build systems disagree about what is in scope, anything excluded from one MUST be covered by the other — see rule 12.

## How to work

| Goal | Flow |
|---|---|
| New feature or bug fix | `/plan "description"` → `/do` → `/git --commit` → `/git --merge` |
| Code review | `/review` |
| Linting | `make lint` (mod-tidy + gazelle drift + gofumpt -l + ktn-linter + alloc-lane coverage + `make guard`) |
| Local test suite | `make build && make test` (build prep + race tests) |
| Allocation gates | `make test-alloc` — race-off pass; the ONLY lane that runs `//go:build !race` tests (targets in `tools/alloc-lane-targets.txt`, see rule 12) |
| Single-package test | `bazel test //<path>:<target>` (e.g. `bazel test //internal/kernel/errs:errs_test`) |
| Regenerate BUILD.bazel | `bazel run //:gazelle` after changing imports or `go.mod` |
| Coverage | `bazel coverage --combined_report=lcov //...` — LCOV at `$(bazel info output_path)/_coverage/_coverage_report.dat` |
| Release dry-run | `make release-dry-run` (computes patch bumps locally without pushing tags — see ADR 0007) |
| Regenerate READMEs | `make docs-readme` (regenerates `pkg/v1/{codec,crypto,errs,hash,sign,kdf,password,logger,logger/writer}/README.md` from package doc comments — see ADR 0008) |

Branch naming matches the conventional commit prefix: `feat/*`, `fix/*`, `refactor/*`, `chore/*`, `docs/*`.

## SDK-wide rules (non-negotiable)

1. **Kernel gate.** A kernel package MUST be stdlib-only AND generic (no domain vocabulary). `level` was moved OUT of kernel because it fails the second half — see ADR 0002 / the layer-placement audit in `.claude/contexts/sdk-layer-placement-audit.md`.
2. **Typed errors only.** Every error returned from SDK code goes through `errs.Define` or `errs.Wrap` (`internal/kernel/errs`). `fmt.Errorf` / `errors.New` are banned in production code. The AST audit (`//internal/kernel/errs:errs_test`, run as part of `make test`) fails the build on violations.
3. **Dotted-quad error codes.** `Code` is a `uint32` laid out `MM.LL.PP.SS` (Major / Layer / Package / Serial) — see ADR 0005. Each package owns a `PP` slot; ADR 0005 §Registry + the ADR 0006 extension (logger v2 + ring) are the authoritative allocation table. Two AST audits enforce it: `registry_external_test.go` checks that no two `Define` calls resolve to the same **value**, and `registry_ownership_external_test.go` checks **range ownership** — one package per `MM.LL.PP`, against a hand-maintained `codeRangeOwners` table that is deliberately independent of the constants it audits (ADR 0035). A new range is allocated in that table in the same change that introduces its codes. Match codes with `errs.HasCode(err, CodeX)` (walks `Unwrap() error` *and* `Unwrap() []error`) or `errors.Is(err, errs.NewPrefixMatcher(...))` for subnet-style routing.
4. **Public/Private split.** Every SDK error carries a wire-safe `Public` (string literal ≤120 runes, no newline) and a log-only `Private`. `err.Error()` renders `"[<code> <REASON>] <public>"` on the no-trail fast path; when the wrap trail is non-empty, ADR 0005 §Semantics extends the bracket header with `" <- "`-separated trail codes and an optional `" (truncated)"` marker — never Private, never Fields. Log-parser regex: `\[[\d.]+(?: <- [\d.]+)*(?: \(truncated\))? \w+\]`.
5. **No empty stub files / dirs.** If a file or directory only carries a placeholder, inline its content into an existing file or delete it.
6. **Origin wins on wrap.** When `errs.Wrap` receives an `*errs.Error` cause, it inherits the cause's Code/Reason/Public/Private. Wrappers can only add `Fields` (and extend the intrinsic wrap trail). To relabel, define a fresh sentinel.
7. **`Version` via build-time injection.** `pkg/v1/logger.Version` is stamped at link time — under Bazel via `x_defs` + `--stamp` + `tools/workspace_status.sh` (`STABLE_VERSION`); under raw `go build` via `-ldflags "-X github.com/kitsunium/sdk/pkg/v1/logger.Version=…"`. `FrameworkVersion()` returns `"dev"` when unset; every emitted log record carries `framework_version` automatically.
8. **Every package is documented.** Pre-commit guard `scripts/pre-commit/check-pkg-docs.sh` blocks the commit when any `internal/*` or `pkg/v*/**` directory containing Go production code is missing `CLAUDE.md` AND `README.md`. Public packages (`pkg/v*/**`) additionally require `README.md` (consumer-facing — pkg.go.dev renders it; the model is `pkg/v1/errs/README.md`). The `scripts/release/*.{sh,mjs}` and `docs/site/scripts/*.mjs` trees are tooling, not library code, and are exempt from this gate.
9. **Every benchmark package ships its numbers.** Pre-commit guard `scripts/pre-commit/check-bench-md.sh` blocks the commit when a directory contains `*_bench_test.go` but no sibling `BENCH.md`. The report is regenerated with `make bench`; it stamps machine, RAM, CPU, OS, Go toolchain, git SHA, and timestamp so cross-machine deltas can be evaluated honestly.
10. **`pkg/v*/**/README.md` are generated, not hand-authored.** The `gomarkdoc` binary (shipped by the devcontainer Go feature, pinned to `@v1.1.0`) reads each package's Go doc comments and emits `README.md` per package (ADR 0008). Edit the package comment in the existing `.go` file (`codec.go` / `accessors.go` / `logger.go`); run `make docs-readme` to regenerate; `make lint` blocks any commit where the file on disk doesn't match what gomarkdoc would produce now. Maintainer rationale (Why-this-shape, layering, do-not lists) stays in `CLAUDE.md` — consumer-facing prose belongs in the package doc comment.
11. **Docs travel with the code — always update them in the same change.** Documentation is part of the change, never a follow-up. Whenever you add/rename/remove an exported symbol, package, format, code range, capability, or convention, update every doc that describes it **in the same commit**: the package's `CLAUDE.md` (Purpose/Surface/Contents/Sentinels), the parent/layer `CLAUDE.md` tables (e.g. `internal/service/codec/CLAUDE.md` Streaming/Appender columns, `internal/core/CLAUDE.md` registry counts), the root `CLAUDE.md` domain list, and — for public packages — the Go doc comment that `gomarkdoc` renders into `README.md` (rule 10). A doc that names a symbol, count, file, or code that no longer matches the code is a defect: fix the doc or the code, never leave them divergent. When in doubt, grep the docs for the old name/number before committing.
12. **Every test excluded from normal discovery needs a named, executable, currently-green gate — or an explicit declaration that it must not run.** Exclusion mechanisms compound silently: `//go:build !race` hides a file from the race suite (race is on by default, see `.bazelrc`), `gazelle:excluded` + `manual` + `-test.run=^$` hides a target from `bazel test //...`, and a `.ktn-linter.yaml` entry hides it from the linter. Each exclusion is individually justified and documented; *together* they have already produced tests that nothing ever ran — including `pkg/v1/logger`'s `TestV116BuildSendAllocatesOnePerEmit`, the regression guard for the allocation claim in this very file. Gates in force today: the race-off alloc lane (`tools/alloc-lane-targets.txt`, mechanically enforced by `scripts/pre-commit/check-alloc-lane-coverage.sh`, wired into `make lint` and CI) covers every `//go:build !race` test; `TestGenerateBenchMD` is opt-in by exact `-test.run` name under BOTH build systems; `integration` / `localstack` tagged tests declare their run procedure in their package `CLAUDE.md`. When you add an exclusion, name its compensating lane in the same commit — and remember that a lane which exists but has been failing for weeks verifies nothing.

After cloning, wire the in-repo hooks with `bash scripts/install-hooks.sh` (one-time per clone — sets `git config core.hooksPath .githooks`).

## Layout

```
/workspace/
├── internal/              see internal/CLAUDE.md
├── pkg/v1/                see pkg/CLAUDE.md + pkg/v1/CLAUDE.md
├── third-party/           opt-in vendor integrations in the root module (AWS writers — ADR 0012)
├── docs/                  ADRs — see docs/CLAUDE.md
├── .devcontainer/         devcontainer infrastructure (template-seeded; leave alone)
├── .github/               CI workflows — bazel-ci.yml is the SDK lane
├── go.work, go.mod        workspace + umbrella module (read by Bazel via from_file)
├── MODULE.bazel           Bzlmod entry point (rules_go 0.60.0 + gazelle 0.50.0 + go_sdk 1.27.0 + go_deps)
├── BUILD.bazel            root gazelle target + audit_sources filegroup
├── .bazelrc               race-on by default; named configs: race / pure / ci / alloc
├── .bazelversion          pins Bazel to 9.0.2
├── Makefile               build / test / test-alloc / lint / bench / cover / docs / serve / release-dry-run / docs-readme … (run `make` for the full list)
├── e2e/                   real-kernel conformance harness — auxiliary module, OUTSIDE go.work (GOWORK=off)
├── tools/sdkguard/        consumer-facing rule enforcement (stdlib-only CLI — ADR 0033)
├── tools/workspace_status.sh  prints STABLE_VERSION (consumed by --stamp + x_defs)
├── tools/alloc-lane-targets.txt  target list for the race-off alloc lane (rule 12)
├── tools/genindex/        docs-site symbol index — auxiliary module, OUTSIDE go.work (GOWORK=off)
├── .golangci.yml          code-quality second-opinion linters (layer firewall is now Bazel visibility)
├── AGENTS.md, agent.toml  devcontainer agent specs (not SDK)
└── README.md              the SDK quickstart (packages, install, verification)
                           — per-package docs live next to their code
```

## Verification

| Command | Expected |
|---|---|
| `ktn-linter lint ./...` | No issues found |
| `bazel build //...` | all targets pass |
| `bazel test --config=race //...` | every `*_test` target green (race on by default — see `.bazelrc`) |
| `bazel coverage --combined_report=lcov //...` | LCOV at `$(bazel info output_path)/_coverage/_coverage_report.dat` |
| `bazel query 'kind("go_library", deps(//internal/kernel/...)) except //internal/kernel/...'` | empty — kernel has zero outgoing go_library edges |
| `make build` | `bazel mod tidy` + `bazel run //:gazelle` + `gofumpt -l -w` + `bazel build //...` |
| `make test` | every `*_test` target green incl. `//internal/kernel/errs:errs_test` (AST audit) |
| `make test-alloc` | race-off allocation gates green (20 targets) — the only lane running `//go:build !race` tests |
| `bash scripts/pre-commit/check-alloc-lane-coverage.sh` | exit 0 — no `!race` test sits outside `tools/alloc-lane-targets.txt` (rule 12) |
| `cd pkg && go test ./...` | green; `pkg/v1/codec` completes in seconds — `TestGenerateBenchMD` self-skips unless named via `-run` |
| `make lint` | drift assertion (read-only): mod tidy + gazelle diff + gofumpt -l + ktn-linter + alloc-lane coverage + `make guard` |
| `make guard` | `tools/sdkguard` over the SDK's own tree at invariant level (ADR 0033); no network — `-version-check=off` |

## Reference

- ADR 0001 — multi-module layout — `docs/adr/0001-sdk-go-multimodule-layout.md`
- ADR 0002 — layered `errs` package — `docs/adr/0002-sdk-errors-package.md` (Registry section superseded by ADR 0005)
- ADR 0003 — universal codec package — `docs/adr/0003-sdk-codec-package.md`
- ADR 0004 — Bazel 9 build system — `docs/adr/0004-sdk-bazel-build-system.md`
- ADR 0005 — dotted-quad error codes + wrap trail — `docs/adr/0005-sdk-error-codes-dotted-quad.md`
- ADR 0006 — error code registry extension (logger v2 + ring) — `docs/adr/0006-sdk-error-code-registry-extension.md`
- ADR 0007 — release workflow + docs versioning — `docs/adr/0007-sdk-release-and-versioning.md`
- ADR 0008 — README generation from Go doc comments — `docs/adr/0008-readme-from-code-generation.md`
- ADR 0009 — public module `go get`-resolvability — `docs/adr/0009-pkg-public-module-resolvability.md`
- ADR 0010 — kernel object-recycling primitive — `docs/adr/0010-kernel-recycler-primitive.md`
- ADR 0011 — kernel copy-on-write snapshot primitive — `docs/adr/0011-kernel-snapshot-primitive.md`
- ADR 0012 — logger writer registry (named, config-driven Sink factories) — `docs/adr/0012-logger-writer-registry.md`
- ADR 0013 — crypto domain (AEAD Seal/Open, hidden nonce; AES-256-GCM default) — `docs/adr/0013-sdk-crypto-domain.md`
- ADR 0014 — the verb wave (core/transform 5th sibling; crypto MAC/Agreement/StreamSealer ports + KeyEnvelope/KeyTree; logger.FromConfig + ConfigDecoder; single-MR sequencing) — `docs/adr/0014-sdk-transform-crypto-ports-config-topology.md`
- ADR 0015 — writer taxonomy + depTier as a first-class property; default writers console+file; rotation off-by-default → compress + 24h archive + 7-day retention; dep-light DB/transport gate (WI-9..WI-11) — `docs/adr/0015-sdk-logger-writer-taxonomy-and-rotation.md`
- ADR 0016 — OS process-supervision domain (`proc`): 6th core sibling; `process`/`signal`/`reaper`/`rlimit`/`cgroup`/`sdnotify` facades; central error block `0.2.6.*`; build-tag platform selection, no registry — `docs/adr/0016-sdk-process-supervision-domain.md`
- ADR 0017 — public module is the bare `…/pkg` (Go forbids the `/v1` module-path suffix); code stays under `pkg/v1/` so imports are unchanged; tag shape `pkg/vX.Y.Z`, first release `v0.1.0` alpha — `docs/adr/0017-pkg-bare-module-path.md`
- ADR 0018 — cross-platform portability strategy: build bar (compiles on all 8 GOOS) + runtime bar (correct on the real kernel); uniform typed `UnsupportedPlatform` where no native mechanic exists; `_linux`/`_unix`/`_bsd`/`_windows`/`_other` split convention; `cross-platform.yml` (build) + `e2e-vm.yml` (real-kernel) gates; OpenBSD `RLIMIT_AS` precedent; native-backend roadmap (FreeBSD `rctl`, Windows Job Objects, BSD `procctl`) — `docs/adr/0018-sdk-cross-platform-portability.md`
- ADR 0019 — public error construction API (`pkg/v1/errs.New`/`Wrap`/`Field` helpers via non-panicking `kernel/errs.NewRuntime`) + third-party Major-byte reservation `0x40–0x7F` (`MinAppMajor`/`MaxMajor`); reverses the `errs` "no constructors" rule so downstreams adopt the error model wholesale — `docs/adr/0019-pkg-errs-public-construction.md`
- ADR 0020 — errs AST audit accepts two Reason derivations (`screamingSnake(varName)` OR `screamingSnake(CodeConst − "Code")`); closes the `//:audit_sources` coverage gap that silently excluded ~10 namespaced emitters (ring + every logger middleware/sink) — `docs/adr/0020-errs-audit-dual-reason-derivation.md`
- ADR 0021 — BSON codec (M5) over `go.mongodb.org/mongo-driver/bson` in `internal/service/codec/bson` (library-backed codec precedent, not third-party quarantine); non-streaming + Appender; error block `0.3.36.*` — `docs/adr/0021-sdk-codec-bson.md`
- ADR 0022 — HCL codec (M5) quarantined in `third-party/codec/hcl` (root module) because `hcl/v2` (via `x/tools`) would **introduce** `x/sys` — banned SDK-wide — into `internal/service`; the ADR's original "downgrades `x/sys`" wording is corrected by ADR 0034; opt-in, not in `pkg/v1/codec`; first `third-party/codec/` subtree; error block `0.3.37.*` — `docs/adr/0022-sdk-codec-hcl.md`
- ADR 0023 — schema codecs (M6): opt-in under `third-party/codec/` (schema-bound, can't honour the universal round-trip contract); Protobuf concrete (`0.3.38.*`), Avro/Cap'n Proto deferred — `docs/adr/0023-sdk-schema-codecs.md`
- ADR 0024 — identifier-generation domain (`id`): 7th core sibling, `Generator`/`Scheme` registry (UUIDv4/v7, ULID, snowflake), canonical-string output, stdlib-only/cross-OS; opens the Phase-B new-domain wave; error block `0.2.7.*`/`0.3.39.*`. Extended in place — NanoID, KSUID and TypeID ship in the same shape and the same `0.3.39.*` block; TypeID is constructor-only (no registered default prefix, ADR 0031) — `docs/adr/0024-sdk-id-domain.md`
- ADR 0025 — generic LRU+TTL `Cache[K,V]` as a **kernel** primitive (`internal/kernel/cache`): domain-neutral, stdlib-only, reuses `clock` for testable TTL; `Fetch` (not `Get`) since a hit mutates LRU; no error codes; `pkg/v1/cache` aliases — `docs/adr/0025-sdk-cache-kernel.md`
- ADR 0026 — reliability domain (`resilience`): 8th core sibling, **no registry** (concrete composable `Runner` policies — retry/circuit-breaker/rate-limit/bulkhead/timeout, later + fallback/hedging); error block `0.2.8.*` — `docs/adr/0026-sdk-resilience-domain.md`
- ADR 0027 — observability domain (`metrics`): 9th core sibling, Counter/Gauge/Histogram + `Meter` + `Exporter` registry (writer-registry model); in-memory meter + the stdlib `text` and `prometheus` exporters; error block `0.2.9.*` (+ `0.3.45.*` for the names the Prometheus wire format refuses); **labels shipped in place** — name + label set = one series, bounded per name with a visible aggregated overflow series (`0.2.9.4` INVALID_LABEL); **the Prometheus text exposition exporter shipped in place too** (stdlib-only, no client library); OTLP + the Prometheus protobuf format stay deferred to `third-party/` — `docs/adr/0027-sdk-metrics-domain.md`
- ADR 0028 — configuration domain (`config`): 10th core sibling, `Source`/`Validator`/`Watcher` ports; env+file layering + JSON round-trip decode + cross-OS poll watcher; closes the Phase-B wave; error block `0.2.10.*` — `docs/adr/0028-sdk-config-domain.md`
- ADR 0029 — network domain (`net`): 11th core sibling covering inbound AND outbound over one substrate (TLS/mTLS identity, per-phase deadlines, policy, metrics); goroutine-per-connection on the runtime netpoller (not an event loop); `net/http` adapted, not reimplemented; `recvmmsg` + `SO_REUSEPORT` via raw stdlib `syscall` because `x/net` pulls in the banned `x/sys`; **no registry**; error block `0.2.11.*` — `docs/adr/0029-sdk-net-domain.md`
- ADR 0030 — stdout is a protocol channel: no SDK default writes to `os.Stdout` (the registered `text` metrics exporter targets stderr; `ConsoleStderr` becomes the `ConsoleStream` zero value). A zero value is the choice made by someone who has not yet learned the question exists — so it must not be the dangerous one — `docs/adr/0030-stdout-is-a-protocol-channel.md`
- ADR 0031 — a policy's zero value is a safe default or an explicit refusal, never an inert policy: **clamp** where a working default needs no explanation, **refuse** where any SDK-chosen value would be arbitrary (`PolicyMisconfigured` `0.2.8.6`) — `docs/adr/0031-policy-zero-values-are-never-inert.md`
- ADR 0032 — `log/slog` is an adapter at the public edge, not a domain dependency: permitted in `pkg/v1/logger/slogbridge` ONLY, so a consumer facing a concrete `*slog.Logger` parameter stops building a second pipeline (two thresholds, two formats, half-stamped records); core keeps its "never log/slog" rule; error block `1.1.1.*` — `docs/adr/0032-logger-slog-bridge.md`
- ADR 0033 — SDK rules are enforced on consumers at BUILD time by `tools/sdkguard`, a stdlib-only CLI: runtime detection was measured impossible (`log/slog` is not a module, a local `slog.Logger` never touches `slog.Default`), and a vettool would need `x/tools`, which `tools/*` cannot take without breaking its Bazel build; rules are split invariant/convention so adoption is incremental — `docs/adr/0033-consumer-rule-enforcement.md`
- ADR 0034 — the HCL quarantine is right, its stated mechanism is not: under MVS a dependency requiring a *lower* version cannot downgrade a higher requirement; measured, adding `hcl/v2` to `internal/service` **introduces** `x/sys` (at `v0.20.0`, via `x/tools`) into a module that **bans** it. Decision unchanged, downgrade narrative retired as a placement rule — `docs/adr/0034-hcl-quarantine-rationale-corrected.md`
- ADR 0035 — PP-range ownership is enforced by an audit the audited code cannot edit: value-uniqueness never saw two packages sharing a `PP` with different `SS`; ownership is keyed on Code *declarations* (not `Define` calls, which miss `core/codec`'s `0.2.2.*`) against a hand-written table, never one generated from the constants — `docs/adr/0035-pp-range-ownership-enforcement.md`
- ADR 0036 — form-urlencoded codec: repetition is the only array syntax (`a=1&a=2` → `["1","2"]`); the `a[]=`/`a[0]=`/comma dialects are framework inventions the format does not define, and last-wins discards a value the wire carried while reporting success; non-bijectivity is stated — the **second** round-trip is the byte-level fixpoint — `docs/adr/0036-sdk-codec-form-urlencoded.md`
- ADR 0037 — multipart/form-data codec: the delimiter lives in the `Content-Type` header, which `Marshal(v any) ([]byte, error)` cannot carry. Rather than widen the contract for one format, the codec domain grows **extension interfaces** (`BoundaryCodec`, `BoundaryProvider`) discovered by type assertion, as `Appender` already is — `docs/adr/0037-sdk-codec-multipart.md`
- ADR 0038 — `id` gains NanoID, KSUID and TypeID, and the `Scheme` registry stops being exhaustive: TypeID is **not** registered because a prefix names the caller's entity, so a singleton would either invent that vocabulary or mint a prefixless identifier — `docs/adr/0038-id-schemes-and-the-unregistered-typeid.md`
- ADR 0039 — a published port is extended by a **sibling interface**, never by widening: `pkg/v1/cache.Config` is a type alias that carries `clock.Clock` into the released module, and Go interfaces are structural, so adding a method breaks every downstream two-method double at compile time with no deprecation window. `Clock` stays frozen, `Waiter` is new, `Timed` is the union, and `System` widens as a *value* — which is safe. Applies to every port an alias publishes — `docs/adr/0039-extending-a-published-port-without-breaking-it.md`
- ADR 0040 — the concrete-type half of ADR 0039: a published **shape** has no sibling trick, so `SnapshotValue` changing form reached every consumer through the `pkg/v1/metrics.Snapshot` alias. Permitted **only because the module is v0** — stated out loud in the commit, and gone at v1. One rule for both halves: an interface gets a sibling at any version; a concrete shape may change while v0, loudly, and not after — `docs/adr/0040-changing-a-published-shape-while-v0.md`
- Layer placement audit — `.claude/contexts/sdk-layer-placement-audit.md`
- Bazel adoption context — `.claude/contexts/bazel-9-go-sdk.md`
