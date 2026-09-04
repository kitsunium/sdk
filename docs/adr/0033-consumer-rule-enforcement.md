# ADR 0033 — A rule the SDK cannot check is a suggestion; enforcement runs at build time, over source

- **Status**: Accepted
- **Date**: 2026-09-04
- **Deciders**: SDK maintainers
- **Related**: [ADR 0032](../adr/0032-logger-slog-bridge.md) (the slog bridge — the rule that prompted this), [ADR 0030](../adr/0030-stdout-is-a-protocol-channel.md), [ADR 0019](../adr/0019-pkg-errs-public-construction.md) (the error model offered to downstreams), [ADR 0004](../adr/0004-sdk-bazel-build-system.md) (why `tools/*` must stay dependency-free)

## Context

ADR 0032 gave consumers a way to stop building a second logging pipeline. It did
not give anyone a way to notice that they still were.

That gap matters more than it sounds. The defect ADR 0032 describes was found in
a downstream service by reading the code, and every symptom it produced was
silent: records dropped because two thresholds were derived by two different
rules, a log file no parser could read, `framework_version` on half the records.
Nothing failed. Nothing warned. The service ran for months.

The SDK's internal rules are all mechanically enforced — the errs AST audit,
Bazel layer visibility, ktn-linter, the alloc-lane coverage guard. Every one of
them stops at the repository boundary. Outside it, the same rules are prose in
an ADR, and prose does not survive contact with a deadline.

The obvious idea — have the SDK detect the violation at runtime, from `init()` —
was tested rather than argued about:

| Probe | Result on a program that DID build a second pipeline |
|---|---|
| `debug.ReadBuildInfo().Deps` | empty; `log/slog` never appears, because the stdlib is not a module |
| `slog.Default().Handler()` | `*slog.defaultHandler`; a `slog.Logger` held in a local variable never touches the global |

Both probes miss the only case worth catching. And the SDK would have to import
`log/slog` to run either one, which is precisely what ADR 0032 confined to a
single package.

Even if a probe existed, crashing would be wrong. A library that panics because
of a *style* violation converts a lint problem into a production outage, and it
would fire on code the consumer does not control: the MCP Go SDK imports
`log/slog` itself.

## Decision

**1. Enforcement is a build-time check over source, shipped by the SDK as
`tools/sdkguard`.**

The SDK writes the rules, so the SDK ships the checker; a rule and its check
version together. Consumers run it in CI. Diagnostics use the standard
`file:line:col` format and the process exits non-zero, so it drops into an
existing lane without new tooling.

**2. It is a plain CLI, not a `go vet -vettool`.**

The vettool protocol lives in `golang.org/x/tools`. `tools/CLAUDE.md` requires
this tree to stay stdlib-only, and that rule is structural rather than
stylistic: Bazel's `go_deps` reads `go.work` and cannot process the extra
modules under `tools/`, so a dependency-free module is exactly what lets Bazel
build them at all (`genindex/` is the precedent). Taking `x/tools` to gain the
`vet` invocation syntax would trade a working build for a nicer command line.
The CI outcome is identical.

**3. Rules target constructs, never imports.**

Banning the `log/slog` import would be simpler and wrong — a consumer of
`slogbridge` imports it to type a `*slog.Logger` field. Only the constructors
and the process-wide default build a second destination. The same reasoning
scopes the stdout rule: ADR 0030 is about stdout carrying a protocol, not about
stdout being forbidden, so `fmt.Fprintln(os.Stdout, …)` in a CLI stays silent
while an `os.Stdout` reaching a `Writer` field does not.

This is the load-bearing decision. A rule that fires on legitimate code teaches
people to ignore the tool, and an ignored tool is worse than none: it converts
"we have no check" into "we have a check", which is a false statement someone
will rely on. The SDK's own `.golangci.yml` already drops `misspell` for that
reason.

**4. Rules carry a level: `invariant` or `convention`.**

An invariant's violation is a correctness defect — records dropped, a protocol
stream corrupted, a binary misreporting what built it. A convention is something
the SDK offers: ADR 0019 makes the error model available to downstreams, it does
not oblige them. `-level=invariant` is what lets a team adopt the tool before it
has adopted every convention, instead of switching the tool off because the
first run printed 21 findings.

**5. Being out of date is warned about, never failed on by default.**

The tool also reads the consumer's `go.mod` and warns when the SDK it pins is
older than the newest release. This belongs here rather than in a changelog
because of how this SDK versions: ADR 0007 cuts a patch whenever an
`internal/*` package that `pkg` depends on changes, so patches carry fixes that
never touch the public API. A consumer reading a changelog of exported symbols
sees nothing and concludes there is nothing to take — the same silence the
rules exist to break.

It is a **warning**: it prints, and the exit code does not move. Being behind is
not a violation, it is a fact the maintainer may already know and may have
decided to live with; a tool that cannot tell those apart should not be the one
stopping the build. `-version-check=error` promotes it for teams that want
freshness gated.

Every failure path degrades to silence — no `go.mod`, a `replace` directive,
`GOPROXY=off`, an unreachable proxy, a 3s timeout, a malformed body. A
freshness nudge that breaks a build has failed at being a nudge, and a linter
that hangs on a firewalled runner is worse than one that skips the check: the
first blocks a pipeline, the second only misses a nudge.

**6. A suppression requires a written reason.**

`//sdkguard:allow SDK001 <reason>`; a bare directive does not suppress. This
mirrors `.ktn-linter.yaml`, whose rule-override block already states the
principle — "every entry here is a design choice we committed to, not 'this
warning is annoying'". An exemption nobody had to justify is the kind that
outlives its reason.

## Consequences / Semantics

- The freshness probe makes one network call per invocation, to a module proxy,
  bounded at 3 seconds. `make lint` passes `-version-check=off`: the SDK is not
  a consumer of itself, and a lint that needs egress fails on an airgapped
  runner.
- Consumers get a checkable contract instead of prose. Run against the service
  that motivated ADR 0032, `-level=invariant` returns exactly two findings, both
  on the line that built the second pipeline, and nothing else.
- The SDK now owns a consumer-facing tool and its false-positive budget. A rule
  added here is a rule imposed on every downstream, so the scoping notes in
  `tools/sdkguard/CLAUDE.md` are part of the contract, not commentary.
- Detection is AST-only, with no type resolution — `go/packages` is in
  `x/tools`. Import aliases are resolved and a selector matches only when the
  file imports the package, but a call re-exported through an intermediate
  wrapper in another module is invisible. That is the honest cost of Decision 2,
  and it is acceptable: the rules exist to catch the accidental second pipeline,
  not to defeat someone determined to hide one.
- Nothing is enforced on a consumer who does not run the tool. This ADR buys a
  check that CAN be run and a reason to run it; it does not buy compliance.

## Breaking changes

- **None for consumers.** `sdkguard` is a new opt-in binary; nothing runs it
  unless a repository chooses to.
- **`make lint` now fails on an SDK invariant violation.** This affects SDK
  contributors, not consumers. The tree is clean at the invariant level today,
  so the gate starts green.

## Alternatives considered

- **`init()`-time detection with a panic or a warning.** Rejected on
  measurement, not taste — see the table above. Both probes miss the real case,
  the SDK would have to import `log/slog` to run them, and crashing on a style
  violation is a production outage bought with a lint finding.
- **A ktn-linter rule.** The natural home, since every repo here already runs
  it — but ktn-linter exposes no project-rule mechanism (only per-rule
  `exclude:` overrides), so it would mean a PR to `kodflow/ktn-linter` and a
  release cycle between writing a rule and enforcing it. Worth revisiting if
  that project gains custom rules.
- **A golangci-lint `forbidigo` snippet consumers copy.** Zero code and it works
  today, but it is per-repo config that drifts, cannot express the
  construct-level scoping of Decision 3 (`depguard` reasons at the import,
  `forbidigo` at the identifier — neither knows a `Writer` field from a
  `Fprintln` argument), and has nowhere to record *why* a rule exists. Kept as a
  fallback for teams that want no new binary.

## Deferred

- **Type-resolved detection.** `go/packages` would close the two known blind
  spots — a call re-exported through a wrapper module, and the field-name
  heuristic that currently requires a logging package's struct type to fire. It
  costs `x/tools`, which Decision 2 rules out for this tree. Revisit only if the
  Bazel constraint changes.
- **Resolving dot imports.** A dot-imported package binds no qualifier, so no
  selector rule can see through it. The rules report the blind spot rather than
  passing quietly; actually analysing such a file needs scope resolution, which
  is the same dependency as above.
- **A ktn-linter rule.** The natural home once that project gains a
  project-rule mechanism; the rules here would move and this binary would
  shrink to the freshness probe.
- **Machine-readable output.** `-format=json` for CI annotators. The
  `file:line:col` text form is already parsed by every annotator in use, so
  this waits for a concrete need.

## References

- [ADR 0004](../adr/0004-sdk-bazel-build-system.md) — why `tools/*` must stay dependency-free
- [ADR 0007](../adr/0007-sdk-release-and-versioning.md) — the patch policy the freshness probe cites
- [ADR 0019](../adr/0019-pkg-errs-public-construction.md) — the error model SDK002 recommends
- [ADR 0030](../adr/0030-stdout-is-a-protocol-channel.md) — the decision SDK003 enforces
- [ADR 0032](../adr/0032-logger-slog-bridge.md) — the decision SDK001 and SDK005 enforce
- `tools/sdkguard/CLAUDE.md` — rule scoping, suppressions, and the known limits
- `tools/CLAUDE.md` — the stdlib-only constraint on this tree and its reason
