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

- Every directory is an independent Go module (see `go.work`); each is individually buildable with `GOWORK=off`.
- Dependency direction is strictly top-down: kernel → core → service → pkg/v1. Enforced by `depguard` in `.golangci.yml`.
- Consumers import only `pkg/v1/*`; `internal/*` is blocked by Go's `internal/` firewall.

## How to work

| Goal | Flow |
|---|---|
| New feature or bug fix | `/plan "description"` → `/do` → `/git --commit` → `/git --merge` |
| Code review | `/review` |
| Linting | `make sdk-lint` (ktn-linter) |
| Local test suite | `make sdk-all` (sync → tidy → lint → errs-audit → test) |

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
├── .github/               CI workflows — sdk-ci.yml is the one SDK-relevant job
├── go.work, go.mod        workspace + umbrella module
├── Makefile               SDK targets: sdk-sync / sdk-tidy / sdk-lint / sdk-errs-audit / sdk-test / sdk-all
├── .golangci.yml          depguard layer firewall
├── AGENTS.md, agent.toml  devcontainer agent specs (not SDK)
└── README.md              SDK quickstart + public API entry point
```

## Verification

| Command | Expected |
|---|---|
| `ktn-linter lint ./...` | No issues found |
| `make sdk-all` | all modules green, coverage ≥ 90% on emitter packages |
| `make sdk-errs-audit` | AST audit passes (literal Public, Reason = var name, code uniqueness) |

## Reference

- ADR 0001 — multi-module layout — `docs/adr/0001-sdk-go-multimodule-layout.md`
- ADR 0002 — layered `errs` package — `docs/adr/0002-sdk-errors-package.md`
- Layer placement audit — `.claude/contexts/sdk-layer-placement-audit.md`
