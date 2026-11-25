<!-- updated: 2026-10-04T00:32:31Z -->
# tools/sdkguard/

> Reference moved to `tools/sdkguard/NOTES.md` (ADR 0253), under the same section titles: Freshness probe, Contents.

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
| SDK004 | invariant | `logger.Version` is stamped at link time, not assigned | `pkg/v1/observe/logger/CLAUDE.md` |
| SDK005 | convention | no second logging pipeline via the legacy `log` package | ADR 0032 |
| SDK006 | convention | no `net/http/pprof` import: it serves profiles from the default mux, unadmitted | ADR 0121 |

**Invariant vs convention** is what makes incremental adoption possible. An
invariant's violation is a correctness defect: records silently dropped, a
protocol stream corrupted, a binary misreporting what built it. A convention is
something the SDK *offers* — ADR 0019 makes the error model available to
downstreams, it does not oblige them. A team runs `-level=invariant` first and
turns conventions on when ready, instead of switching the whole tool off.

**The SDK holds itself to SDK002.** SDK rule 2 (root `CLAUDE.md`: no
`fmt.Errorf`, no `errors.New` in production code) used to cite an AST audit
that did not exist, and 22 calls had passed CI. `make guard` — run by
`make lint` and by CI's lint gate — now runs a second pass, `-rules=SDK002`,
over `internal/`, `pkg/`, `third-party/` and `framework/`. Selecting a rule by
ID runs it whatever its level, so the level a consumer gets by default does not
move. `tools/` and `e2e/` are outside that pass: stdlib-only modules, they
cannot import the errs package the rule sends a caller to (`errors.go` here is
the example). The SDK grants itself no exemption — `make guard` fails on a
`//sdkguard:allow SDK002` directive in that tree, and fails closed when it
cannot scan for one.

**The SDK holds itself to SDK006 too (#1023).** `net/http/pprof`'s `init`
registers six handlers on `http.DefaultServeMux` the moment the package is
linked, under no admission, and `pprof.Handler(name)` does not avoid it: the
import runs the `init` anyway. The SDK serves the same wire through
`profiling.NewHandler`, a plain `http.Handler` the caller mounts behind its own
link, and a repository that imported the package once would hand every product
built on it an open `/debug/pprof/`. For a consumer SDK006 stays a convention —
a private debug listener may want the stdlib's — and `make guard` runs a
third pass, `-rules=SDK006 -tests`, over every tree the SDK writes Go in
(`internal/`, `pkg/`, `third-party/`, `framework/`, `tools/`, `e2e/`) and test
files included, since a test binary that links it registers the same routes.
The rule reads the import block itself, because the usual spelling is
`import _ "net/http/pprof"`, which binds no name: `importsOf`, the resolution
every other rule goes through, drops a blank import by design.

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
  missed finding where a file both shadows the name and violates the rule. A
  struct field or an interface method is NOT such a declaration
  (`memberFields`): it is reached through a selector, never bare. Counting one
  used to silence every rule in any file with a field named `logger` or
  `errors` — and let an `errors.New` past the SDK's own gate.

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
