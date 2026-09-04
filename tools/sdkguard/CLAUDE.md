# tools/sdkguard/

## Purpose

Enforces the SDK's **consumer-facing** rules on a codebase that imports the SDK.
The ADRs state decisions — one logging pipeline, typed errors, stdout belongs to
the protocol — and this is what makes them checkable in a repo the SDK does not
own.

Stdlib-only, no `go.sum`, outside `go.work` (the `tools/` convention). Reports on
stderr in the standard `file:line:col` format and exits non-zero, so it drops
into an existing CI line without new tooling.

## Why a build-time CLI and not an `init()` or a vettool

**Runtime detection is impossible**, and that was measured before the tool was
written, not assumed:

| Probe | Result |
|---|---|
| `debug.ReadBuildInfo().Deps` | `log/slog` never appears — the stdlib is not a module, so `Deps` is empty for a stdlib-only binary |
| `slog.Default().Handler()` | still `*slog.defaultHandler` in a program that built a second pipeline — a `slog.Logger` held in a local variable never touches the global |

The only case worth catching is invisible to both probes. And a library that
panics at init because of a *style* violation converts a lint problem into a
production outage, on top of guaranteeing false positives: the MCP Go SDK
imports `log/slog` itself, so a naive check would punish a consumer for a
dependency's legitimate use.

**A `go vet -vettool` was rejected for a structural reason, not a stylistic one.**
The vettool protocol lives in `golang.org/x/tools`, and `tools/CLAUDE.md`
requires this tree to stay stdlib-only. That rule is not decoration: a
dependency-free module is exactly what lets Bazel build `tools/*` while they sit
outside `go.work` (`go_deps` reads `go.work` and cannot process extra modules).
`genindex/` is the precedent — zero deps, its own `BUILD.bazel`. The CI outcome
is identical either way: diagnostics and a non-zero exit.

## Rules

| ID | Level | Rule | Source |
|---|---|---|---|
| SDK001 | invariant | no second logging pipeline beside the SDK logger | ADR 0032 |
| SDK002 | convention | errors carry a typed code, not a formatted string | SDK rule 2 / ADR 0019 |
| SDK003 | invariant | stdout is a protocol channel, never a log destination | ADR 0030 |
| SDK004 | invariant | `logger.Version` is stamped at link time, not assigned | `pkg/v1/logger/CLAUDE.md` |
| SDK005 | convention | no second logging pipeline via the legacy `log` package | ADR 0032 |

**Invariant vs convention** is what makes incremental adoption possible. An
invariant's violation is a correctness defect: records silently dropped, a
protocol stream corrupted, a binary misreporting what built it. A convention is
something the SDK *offers* — ADR 0019 makes the error model available to
downstreams, it does not oblige them. A team runs `-level=invariant` first and
turns conventions on when ready, instead of switching the whole tool off.

## Scoping — why each rule is narrower than it looks

The rules are deliberately narrow. A rule that fires on legitimate code teaches
people to ignore the tool, which costs more than the rule was worth; the SDK's
own `.golangci.yml` drops `misspell` for exactly that reason.

- **SDK001 does not ban the `log/slog` import.** That would be simpler and
  wrong: a consumer of `slogbridge` imports it to type a `*slog.Logger` field or
  to pass `slog.String` attrs to a foreign API. Only the constructors
  (`New`, `NewTextHandler`, `NewJSONHandler`) and the process-wide default
  (`SetDefault`, `Default`) create a second destination.
- **SDK003 does not ban `os.Stdout`.** ADR 0030 is about stdout carrying a
  protocol, not about stdout being forbidden. The rule fires only where stdout
  becomes a *log destination* — a `Writer`/`Writers`/`Out`/`Output` field, or an
  argument to a logging constructor. `fmt.Fprintln(os.Stdout, …)` and
  `json.NewEncoder(os.Stdout)` are a CLI doing its job and stay silent.
- **Test files are excluded by default** (`-tests` opts in): fixtures
  legitimately mint throwaway errors.

## Suppressions

```go
slg := slog.New(h) //sdkguard:allow SDK001 vendor API needs a raw handler
```

The reason is **mandatory** — a bare `//sdkguard:allow SDK001` does not
suppress. An exemption nobody had to justify is the kind that outlives the
reason it was granted for, which is the failure mode `.ktn-linter.yaml` warns
about in its own rule-override block. A directive counts on the finding's line
or the line above it, matching how `//nolint` is already written.

## Contents

| File | Role |
|---|---|
| `main.go` | CLI entry, flags (`-tests`, `-rules`, `-level`, `-list`), exit codes |
| `analyze.go` | walking, parsing, import-alias resolution, suppression directives |
| `rules.go` | the rule table and the five checks |
| `sdkguard_test.go` | one fire + one silence case per rule, plus alias, suppression, level and ordering |

Exit codes: `0` clean, `1` findings, `2` the tool itself failed — so CI can tell
"rules broken" from "tool broke".

## Known limit

Detection is AST-only, with no type resolution: `go/types` needs a package
loader, and the usable one (`go/packages`) is in `x/tools`. Import aliases are
resolved, and a selector only matches when the file actually imports the package
— so a local named `fmt` in a file that does not import `fmt` is not flagged
(pinned by a test). What AST-only cannot see is a package **re-exported** through
an intermediate wrapper: `mylog.New()` that wraps `slog.New()` inside another
module is invisible here. That is the honest trade for staying dependency-free,
and it is the right one: the rules exist to catch the accidental second
pipeline, not to defeat someone determined to hide one.

## Do NOT

- Add a dependency. It would break the Bazel build of `tools/*` (see above), not
  merely the convention.
- Widen a rule to the whole import. Every rule here targets a *construct*; the
  scoping notes above are the design, not an optimisation.
- Make a suppression work without a reason.
- Wire this as a runtime check in the SDK. The measurement at the top of this
  file is why.

## Verification

```bash
bazel test --config=race //tools/sdkguard:sdkguard_test
# Fallback
cd tools/sdkguard && GOWORK=off go test -race -cover ./...

# Run it against a consumer
go run github.com/kitsunium/sdk/tools/sdkguard@latest -level=invariant ./...
```
