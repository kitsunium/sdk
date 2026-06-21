<!-- updated: 2026-05-21T21:27:56Z -->
# kitsunium/sdk

## Purpose

Go SDK providing a normed, performant toolbox for downstream applications. Six domains ship today — a structured **logger** (zero-alloc, multi-sink), a universal **codec** (19 wire formats behind a single `Marshal/Unmarshal` dispatch), typed **errs** (dotted-quad codes + public/private split), a **crypto** suite (AEAD, hash, sign, MAC, KDF, key-agreement, password hashing), **transform** (compression), and OS **proc** supervision. New domains land in the same 4-layer shape (ADR 0001).

**Repository**: `github.com/kitsunium/sdk` · **Module name**: same · **Go**: 1.26.2 (pinned in `MODULE.bazel`)

## Architecture at a glance

```
internal/
├── kernel/        stdlib-only AND generic primitives
│                  errs, recycler, snapshot, buffer, clock, ring
├── core/          domain interfaces + domain values
│                  codec, writer, crypto, logger, logger/level
└── service/       concrete implementations
                   logger (+ encoder, sink/{console,file,syslog},
                             middleware/{multi,async,route,
                                         failover,sample,recover})
                   writer (console, file, levelgate)
                   crypto (aesgcm)
                   codec  (asn1, cbor, csv, json, msgpack,
                           ndjson, pem, toml, xml, yaml)
pkg/
└── v1/            stable public API (type aliases + ergonomic helpers)
    ├── logger/    (+ ldflags-injected Version)
    ├── errs/      (read-only introspection: CodeOf, ReasonOf, …)
    └── codec/     (blank-imports all 10 registrations)
        └── baseenc/   (base16/32/64 wrappers, not a codec)
```

- Five independent Go modules held together by `go.work`: root (umbrella — also hosts the opt-in, vendor-dependent integrations under `third-party/*`, e.g. the AWS writers; see ADR 0012), `internal/kernel`, `internal/core`, `internal/service`, and the public `pkg` (module `github.com/kitsunium/sdk/pkg`, `go.mod` at `pkg/go.mod`; its consumer packages live under `pkg/v1/` and import as `…/pkg/v1/*`, but the *module* is the bare `…/pkg` because Go forbids a `/v1` module-path suffix — ADR 0017). Each module-local `go.mod` carries `replace` directives so `GOWORK=off go build ./...` per-module still works. Heavy vendor deps (AWS SDK) live in the **root** `go.mod` only — nothing requires the root module, so `pkg` consumers stay dep-light.
- Dependency direction is strictly top-down: kernel → core → service → pkg/v1. Enforced by Bazel `package_group` + `visibility` (see ADR 0004). A rogue import fails `bazel build` before it ever reaches the linter.
- Consumers import only `pkg/v1/*`; `internal/*` is blocked by Go's `internal/` firewall AND by the Bazel layer visibility.
- Build / test / lint go through **Bazel 9** — see ADR 0004. `go test ./...` still works locally for quick iteration but CI only runs `bazel`.

## How to work

| Goal | Flow |
|---|---|
| New feature or bug fix | `/plan "description"` → `/do` → `/git --commit` → `/git --merge` |
| Code review | `/review` |
| Linting | `make lint` (mod-tidy + gazelle drift + gofumpt -l + ktn-linter) |
| Local test suite | `make build && make test` (build prep + race tests) |
| Single-package test | `bazel test //<path>:<target>` (e.g. `bazel test //internal/kernel/errs:errs_test`) |
| Regenerate BUILD.bazel | `bazel run //:gazelle` after changing imports or `go.mod` |
| Coverage | `bazel coverage --combined_report=lcov //...` — LCOV at `$(bazel info output_path)/_coverage/_coverage_report.dat` |
| Release dry-run | `make release-dry-run` (computes patch bumps locally without pushing tags — see ADR 0007) |
| Regenerate READMEs | `make docs-readme` (regenerates `pkg/v1/{codec,errs,logger}/README.md` from package doc comments — see ADR 0008) |

Branch naming matches the conventional commit prefix: `feat/*`, `fix/*`, `refactor/*`, `chore/*`, `docs/*`.

## SDK-wide rules (non-negotiable)

1. **Kernel gate.** A kernel package MUST be stdlib-only AND generic (no domain vocabulary). `level` was moved OUT of kernel because it fails the second half — see ADR 0002 / the layer-placement audit in `.claude/contexts/sdk-layer-placement-audit.md`.
2. **Typed errors only.** Every error returned from SDK code goes through `errs.Define` or `errs.Wrap` (`internal/kernel/errs`). `fmt.Errorf` / `errors.New` are banned in production code. The AST audit (`//internal/kernel/errs:errs_test`, run as part of `make test`) fails the build on violations.
3. **Dotted-quad error codes.** `Code` is a `uint32` laid out `MM.LL.PP.SS` (Major / Layer / Package / Serial) — see ADR 0005. Each package owns a `PP` slot; ADR 0005 §Registry + the ADR 0006 extension (logger v2 + ring) are the authoritative allocation table, mirrored by the AST audit in `internal/kernel/errs/registry_external_test.go`. Match codes with `errs.HasCode(err, CodeX)` (walks `Unwrap() error` *and* `Unwrap() []error`) or `errors.Is(err, errs.NewPrefixMatcher(...))` for subnet-style routing.
4. **Public/Private split.** Every SDK error carries a wire-safe `Public` (string literal ≤120 runes, no newline) and a log-only `Private`. `err.Error()` renders `"[<code> <REASON>] <public>"` on the no-trail fast path; when the wrap trail is non-empty, ADR 0005 §Semantics extends the bracket header with `" <- "`-separated trail codes and an optional `" (truncated)"` marker — never Private, never Fields. Log-parser regex: `\[[\d.]+(?: <- [\d.]+)*(?: \(truncated\))? \w+\]`.
5. **No empty stub files / dirs.** If a file or directory only carries a placeholder, inline its content into an existing file or delete it.
6. **Origin wins on wrap.** When `errs.Wrap` receives an `*errs.Error` cause, it inherits the cause's Code/Reason/Public/Private. Wrappers can only add `Fields` (and extend the intrinsic wrap trail). To relabel, define a fresh sentinel.
7. **`Version` via build-time injection.** `pkg/v1/logger.Version` is stamped at link time — under Bazel via `x_defs` + `--stamp` + `tools/workspace_status.sh` (`STABLE_VERSION`); under raw `go build` via `-ldflags "-X github.com/kitsunium/sdk/pkg/v1/logger.Version=…"`. `FrameworkVersion()` returns `"dev"` when unset; every emitted log record carries `framework_version` automatically.
8. **Every package is documented.** Pre-commit guard `scripts/pre-commit/check-pkg-docs.sh` blocks the commit when any `internal/*` or `pkg/v*/**` directory containing Go production code is missing `CLAUDE.md` AND `README.md`. Public packages (`pkg/v*/**`) additionally require `README.md` (consumer-facing — pkg.go.dev renders it; the model is `pkg/v1/errs/README.md`). The `scripts/release/*.{sh,mjs}` and `docs/site/scripts/*.mjs` trees are tooling, not library code, and are exempt from this gate.
10. **`pkg/v*/**/README.md` are generated, not hand-authored.** The `gomarkdoc` binary (shipped by the devcontainer Go feature, pinned to `@v1.1.0`) reads each package's Go doc comments and emits `README.md` per package (ADR 0008). Edit the package comment in the existing `.go` file (`codec.go` / `accessors.go` / `logger.go`); run `make docs-readme` to regenerate; `make lint` blocks any commit where the file on disk doesn't match what gomarkdoc would produce now. Maintainer rationale (Why-this-shape, layering, do-not lists) stays in `CLAUDE.md` — consumer-facing prose belongs in the package doc comment.
9. **Every benchmark package ships its numbers.** Pre-commit guard `scripts/pre-commit/check-bench-md.sh` blocks the commit when a directory contains `*_bench_test.go` but no sibling `BENCH.md`. The report is regenerated with `make bench`; it stamps machine, RAM, CPU, OS, Go toolchain, git SHA, and timestamp so cross-machine deltas can be evaluated honestly.
11. **Docs travel with the code — always update them in the same change.** Documentation is part of the change, never a follow-up. Whenever you add/rename/remove an exported symbol, package, format, code range, capability, or convention, update every doc that describes it **in the same commit**: the package's `CLAUDE.md` (Purpose/Surface/Contents/Sentinels), the parent/layer `CLAUDE.md` tables (e.g. `internal/service/codec/CLAUDE.md` Streaming/Appender columns, `internal/core/CLAUDE.md` registry counts), the root `CLAUDE.md` domain list, and — for public packages — the Go doc comment that `gomarkdoc` renders into `README.md` (rule 10). A doc that names a symbol, count, file, or code that no longer matches the code is a defect: fix the doc or the code, never leave them divergent. When in doubt, grep the docs for the old name/number before committing.

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
├── MODULE.bazel           Bzlmod entry point (rules_go 0.60.0 + gazelle 0.50.0 + go_sdk 1.26.2 + go_deps)
├── BUILD.bazel            root gazelle target + audit_sources filegroup
├── .bazelrc               race-on by default; named configs: race / pure / coverage / ci
├── .bazelversion          pins Bazel to 9.0.2
├── Makefile               build / test / lint / bench / cover / docs / serve / release-dry-run / docs-readme … (run `make` for the full list)
├── tools/workspace_status.sh  prints STABLE_VERSION (consumed by --stamp + x_defs)
├── .golangci.yml          code-quality second-opinion linters (layer firewall is now Bazel visibility)
├── AGENTS.md, agent.toml  devcontainer agent specs (not SDK)
└── README.md              devcontainer-template readme; NOT the SDK quickstart
                           (SDK package docs live next to their code)
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
| `make lint` | drift assertion (read-only): mod tidy + gazelle diff + gofumpt -l + ktn-linter |

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
- Layer placement audit — `.claude/contexts/sdk-layer-placement-audit.md`
- Bazel adoption context — `.claude/contexts/bazel-9-go-sdk.md`
