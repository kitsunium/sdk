<!-- updated: 2026-04-19T10:18:42Z -->
# kitsunium/sdk

## Purpose

Go SDK providing a normed, performant toolbox for downstream applications. The first tool shipped is a structured logger; more domains land in the same 4-layer shape.

**Repository**: `github.com/kitsunium/sdk` · **Module name**: same · **Go**: 1.26

## Architecture at a glance

```
internal/
├── kernel/        stdlib-only AND generic primitives  (errs, clock, buffer)
├── core/          domain interfaces + domain values   (logger, logger/level)
└── service/       concrete implementations            (logger/text handler)
pkg/
└── v1/            stable public API (type aliases + ergonomic helpers)
    ├── logger/
    └── errs/
```

- Every directory is an independent Go module (see `go.work`); each is individually buildable with `GOWORK=off` (useful when debugging outside Bazel).
- Dependency direction is strictly top-down: kernel → core → service → pkg/v1. Enforced by Bazel `package_group` + `visibility` (see ADR 0004). A rogue import fails `bazel build` before it ever reaches the linter.
- Consumers import only `pkg/v1/*`; `internal/*` is blocked by Go's `internal/` firewall AND by the Bazel layer visibility.
- Build / test / lint go through **Bazel 9** — see ADR 0004. `go test ./...` still works locally for quick iteration but CI only runs `bazel`.

## How to work

| Goal | Flow |
|---|---|
| New feature or bug fix | `/plan "description"` → `/do` → `/git --commit` → `/git --merge` |
| Code review | `/review` |
| Linting | `make sdk-lint` (mod-tidy + gazelle drift) or `ktn-linter lint ./...` |
| Local test suite | `make sdk-all` → shells to `bazel mod tidy`, `bazel run //:gazelle`, `bazel test --config=race //...` |
| Single-package test | `bazel test //<path>:<target>` (e.g. `bazel test //internal/kernel/errs:errs_test`) |
| Regenerate BUILD.bazel | `bazel run //:gazelle` after changing imports or `go.mod` |
| Coverage | `bazel coverage --combined_report=lcov //...` — LCOV at `$(bazel info output_path)/_coverage/_coverage_report.dat` |

Branch naming matches the conventional commit prefix: `feat/*`, `fix/*`, `refactor/*`, `chore/*`, `docs/*`.

## SDK-wide rules (non-negotiable)

1. **Kernel gate.** A kernel package MUST be stdlib-only AND generic (no domain vocabulary). `level` was moved OUT of kernel because it fails the second half — see ADR 0002 / the layer-placement audit in `.claude/contexts/sdk-layer-placement-audit.md`.
2. **Typed errors only.** Every error returned from SDK code goes through `errs.Define` or `errs.Wrap` (`internal/kernel/errs`). `fmt.Errorf` / `errors.New` are banned in production code. The AST audit (`make sdk-errs-audit`) fails the build on violations.
3. **Public/Private split.** Every SDK error carries a wire-safe `Public` (string literal ≤120 runes) and a log-only `Private`. `err.Error()` returns `"[<code> <REASON>] <public>"` — never Private, never Fields.
4. **No empty stub files / dirs.** If a file or directory only carries a placeholder, inline its content into an existing file or delete it. Enforced by convention and by the "feedback_no_empty_stub_files" rule in the agent's memory.
5. **Origin wins on wrap.** When `errs.Wrap` receives an `*errs.Error` cause, it inherits the cause's Code/Reason/Public/Private. Wrappers can only add `Fields`. To relabel, define a fresh sentinel.
6. **`Version` via ldflags.** `pkg/v1/logger.Version` is injected at build time via `-ldflags "-X github.com/kitsunium/sdk/pkg/v1/logger.Version=…"`; every emitted record carries `framework_version` automatically.

## Layout

```
/workspace/
├── internal/              see internal/CLAUDE.md
├── pkg/v1/                see pkg/CLAUDE.md + pkg/v1/CLAUDE.md
├── docs/                  ADRs — see docs/CLAUDE.md
├── .devcontainer/         devcontainer infrastructure (template-seeded; leave alone)
├── .github/               CI workflows — bazel-ci.yml is the SDK job
├── go.work, go.mod        workspace + umbrella module (read by Bazel via from_file)
├── MODULE.bazel           Bzlmod entry point (rules_go + gazelle + go_sdk + go_deps)
├── BUILD.bazel            root gazelle target + audit_sources filegroup
├── .bazelrc               named configs: race / pure / coverage / ci
├── .bazelversion          pins Bazel to 9.0.2
├── Makefile               SDK targets: wrappers over bazel mod tidy / run //:gazelle / test / coverage
├── .golangci.yml          code-quality second-opinion linters (layer firewall is now Bazel visibility)
├── AGENTS.md, agent.toml  devcontainer agent specs (not SDK)
└── README.md              SDK quickstart + public API entry point
```

## Verification

| Command | Expected |
|---|---|
| `ktn-linter lint ./...` | No issues found |
| `bazel build //...` | 63+ targets, all pass |
| `bazel test --config=race //...` | 20/20 tests pass (race on) |
| `bazel coverage --combined_report=lcov //...` | LCOV at `bazel-out/_coverage/_coverage_report.dat` |
| `bazel query 'kind("go_library", deps(//internal/kernel/...)) except //internal/kernel/...'` | empty — kernel has zero outgoing go_library edges |
| `make sdk-all` | wraps `bazel mod tidy` + `bazel run //:gazelle` + `bazel test --config=race //...` |
| `make sdk-errs-audit` | AST audit passes (runs under Bazel via `//internal/kernel/errs:errs_test`) |

## Reference

- ADR 0001 — multi-module layout — `docs/adr/0001-sdk-go-multimodule-layout.md`
- ADR 0002 — layered `errs` package — `docs/adr/0002-sdk-errors-package.md`
- ADR 0003 — universal codec package — `docs/adr/0003-sdk-codec-package.md`
- ADR 0004 — Bazel 9 build system — `docs/adr/0004-sdk-bazel-build-system.md`
- Layer placement audit — `.claude/contexts/sdk-layer-placement-audit.md`
- Bazel adoption context — `.claude/contexts/bazel-9-go-sdk.md`
