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

## Freshness probe

Beside the rules, sdkguard warns when the consumer's `go.mod` pins an SDK older
than the newest release:

```
sdkguard: warning: the SDK is 15 patch releases behind — go.mod requires v0.1.9, latest is v0.1.24.
  A patch is cut whenever an internal package pkg depends on changes (ADR 0007),
  so releases carry fixes that never alter the public API.
  Update:  go get github.com/kitsunium/sdk/pkg@v0.1.24
  Silence: -version-check=off
```

This exists because of how the SDK versions. Per ADR 0007 a patch is cut
whenever an `internal/*` package that `pkg` depends on changes — so patches
carry fixes that never touch the public API. A consumer reading a changelog of
exported symbols sees nothing and concludes there is nothing to take. That is
the quiet cousin of the defects the rules catch: nothing fails, and the fix
simply never arrives.

**In the default mode it is a warning, and the exit code does not move.** Being behind is not a
violation — it is a fact the maintainer may already know and may have decided to
live with. `-version-check=error` promotes it for teams that want freshness
gated; `-version-check=off` skips the probe entirely, making no network call.

**Every failure path degrades to silence**, because a freshness nudge that
breaks a build has failed at being a nudge:

| Situation | Behaviour |
|---|---|
| No `go.mod`, or no SDK requirement | silent |
| A `replace` directive on the SDK | silent — that is a local checkout, not a stale pin |
| `GOPROXY=off`, or a proxy list with no usable URL | silent, no network call |
| Primary proxy down, backup listed | falls through to the next entry — the list is walked in order |
| A workspace root with no `go.mod` | resolved through `go.work`; the OLDEST module's requirement is reported, so a stale module cannot hide behind an up-to-date sibling |
| Several roots given | each module is probed once; results do not depend on argument order |
| Proxy unreachable, timeout (3s), non-200, malformed body | silent |
| Consumer ahead of the proxy (unreleased local tag) | silent |

`GOPROXY` is read the way the go command reads it — `,`/`|` fallback lists,
`off`, `direct`. Only strict `vX.Y.Z` tags are candidates: recommending a
prerelease or a pseudo-version as "the latest" would be wrong advice. Ordering
is numeric, not lexical, so `v0.1.9` correctly precedes `v0.1.24` — a string
comparison would invert that and tell an up-to-date consumer to downgrade.

`make guard` passes `-version-check=off`: the SDK is not a consumer of itself,
and `make lint` must not depend on network egress.

## Scoping — why each rule is narrower than it looks

The rules are deliberately narrow. A rule that fires on legitimate code teaches
people to ignore the tool, which costs more than the rule was worth; the SDK's
own `.golangci.yml` drops `misspell` for exactly that reason.

- **SDK001 does not ban the `log/slog` import.** That would be simpler and
  wrong: a consumer of `slogbridge` imports it to type a `*slog.Logger` field or
  to pass `slog.String` attrs to a foreign API. Only the constructors
  (`New`, `NewTextHandler`, `NewJSONHandler`) and the process-wide default
  (`SetDefault`, `Default`) create a second destination.
- **SDK003 does not ban `os.Stdout`, and the field name alone concludes
  nothing.** ADR 0030 is about stdout carrying a protocol, not about stdout
  being forbidden. The rule fires only where stdout becomes a *log
  destination*: a `Writer`/`Writers`/`Out`/`Output` field **of a struct whose
  type comes from a logging package** (`logger.Config`, `slog.HandlerOptions`,
  …), or an argument to a logging constructor. `fmt.Fprintln(os.Stdout, …)`,
  `json.NewEncoder(os.Stdout)` and a plain `Report{Output: os.Stdout}` are a CLI
  doing its job and stay silent. Requiring the literal's type is what keeps the
  field-name heuristic from firing on every struct that happens to have an
  `Output`; the cost is that a consumer's own logging wrapper is missed, which
  is the documented price of working without type resolution.
- **Test files are excluded by default** (`-tests` opts in): fixtures
  legitimately mint throwaway errors.
- **The sanctioned bridge composition is not a second pipeline.** SDK001 skips
  `slog.New` when its argument came from `slogbridge.NewHandler` — inline, or
  bound to a variable first, which is the form real code uses since
  `NewHandler` returns `(handler, error)`. Firing there would fail an invariant
  on the very construction the rule points people towards.
- **Files outside the consumer's control are skipped.** A `//go:build ignore`
  file is in no build, and a `// Code generated … DO NOT EDIT.` file is not
  theirs to fix; reporting either is noise they cannot action. A
  *platform*-constrained file (`//go:build linux`) IS still analysed — its
  rules apply on the platform it targets.
- **A dot import is reported, not ignored.** `import . "log/slog"` binds no
  qualifier, so `slog.New` appears as `New` and no selector rule can see it.
  Every rule that watches a dot-imported package emits a finding saying it
  cannot analyse the file. A rule that cannot see is not a rule that found
  nothing, and only the first deserves a clean run.

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
| `main.go` | CLI entry, flags (`-tests`, `-rules`, `-level`, `-version-check`, `-list`), exit codes |
| `analyze.go` | walking, parsing, import-alias resolution, suppression directives |
| `rules.go` | the rule table and the five checks |
| `version.go` | the freshness probe: go.mod reading, GOPROXY resolution, semver ordering |
| `errors.go` | `errProxyDisabled` — sdkguard cannot use `pkg/v1/errs`, since it must run without pulling the library it audits |
| `sdkguard_test.go` | one fire + one silence case per rule, plus alias, suppression, level and ordering |
| `version_test.go` | semver ordering, go.mod shapes, GOPROXY resolution, and the degrade-to-silence paths — served by `httptest`, so no test touches the network |

Exit codes: `0` clean, `1` findings, `2` the tool itself failed — so CI can tell
"rules broken" from "tool broke".

## Known limit

Detection is AST-only, with no type resolution: `go/types` needs a package
loader, and the usable one (`go/packages`) is in `x/tools`. Import aliases are
resolved, and a selector only matches when the file actually imports the package
— so a local named `fmt` in a file that does not import `fmt` is not flagged
(pinned by a test). Three consequences of AST-only analysis:

- A package **re-exported** through an intermediate wrapper: `mylog.New()`
  wrapping `slog.New()` inside another module is invisible here. Silent.
- A **dot import**, which binds no qualifier at all. Not silent — each affected
  rule says it cannot analyse the file.
- A **shadowed package name**: `logger := myLog{}` followed by
  `logger.Version = "x"` is a field assignment on a local, indistinguishable
  from a write to the SDK's package variable without type resolution. A package
  whose local name the file also declares stops matching there. Silent, and
  deliberately so — `logger` is a name consumers bind constantly, and a rule
  that fires on correct code is the kind people switch off. The cost is a
  missed finding where a file both shadows the name and violates the rule.

That is the honest trade for staying dependency-free, and it is the right one:
the rules exist to catch the accidental second pipeline, not to defeat someone
determined to hide one.

## Do NOT

- Add a dependency. It would break the Bazel build of `tools/*` (see above), not
  merely the convention.
- Widen a rule to the whole import. Every rule here targets a *construct*; the
  scoping notes above are the design, not an optimisation.
- Make a suppression work without a reason.
- Let the freshness probe fail a run by default, or make any of its failure
  paths loud. It is a nudge; the moment it can break CI it stops being one.
- Add the probe to `make lint` without `-version-check=off`. A lint that needs
  the network is a lint that fails on an airgapped runner.
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
