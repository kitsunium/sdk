# ADR 0169 — the SDK is rebuilt from its design in a copy, and a generation is held to the whole inline set

- **Status**: Accepted
- **Date**: 2026-10-05
- **Deciders**: SDK maintainers (the owner's decision: stage 7, the last, of "kit regenerates the Go SDK from its design, and generates the docs and the most performant code possible")
- **Amends**: [ADR 0168](0168-a-declaration-is-the-designs-and-a-body-is-the-codes.md) — §Deferred's first item closed: kit measures the project's inline set, not only the functions the design names; [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md) — `make regen` gains a sibling that needs no clean tree
- **Related**: [ADR 0165](0165-a-performance-contract-is-the-designs-kit-measures-what-the-compiler-decides-and-a-test-holds-the-rest.md) (what the compiler decides is measured), [ADR 0166](0166-a-facade-is-the-designs-and-a-forwarders-form-is-measured.md) (a forwarder's form is measured), [ADR 0167](0167-a-doc-comment-is-the-designs-and-a-readme-is-written-from-docs-api.md)

## Context

Stages 1 to 6 moved into `design/` every exported signature, doc comment,
port, error code, facade, performance contract and — for `semver` and
`clock` — the declarations themselves, and kit writes all of it. Two
questions were left open.

**What a wrapper costs reaches past what the design names.** ADR 0168 §2
measures a wrapper, its impl and every `pkg/v1` forwarder of it. A wrapper
also adds the cost of one more inlined call to *every* function of the
project that inlines it, and a function sitting just under the inliner's
budget of 80 goes over. Converting `kernel/backoff` met exactly that: as
wrappers, its normalisers pushed `service/app/resilience.NewRetry` past the
budget, which nothing kit measured — found by comparing the whole module's
`-m` report by hand (ADR 0168 §3, §Deferred).

**"Rebuilt from the design" was only ever shown in place.** `make regen`
deletes every generated file in the working tree and requires zero diff, so
it needs a clean tree and never shows the state in between: the design and
the hand-written bodies, and nothing else. And nobody had counted how much of
the SDK's exported surface kit actually writes.

## Decision

### 1. A generation is held to the project's inline set (kit `v0.1.0-rc.7`)

kit compares two states of the whole project, compiler only:
`go build -gcflags=<module>/...=-m=2 <module>/...` for every module of the
census, in the design's first cell (`linux/amd64`), each state given to the
go command as an overlay so nothing is written. The **base** is the project
at a git ref (`HEAD`, or `-base REF` on `kit gen` and `kit check`); the
**result** is what `kit gen` would write, or, for `kit check`, the files on
disk. A function of the project — not a closure, a range body or a generic
instantiation, whose names are the compiler's own numbering — that the base
inlines and the result does not has lost its inlinability, and is named with
its cost before and after, the delta, and the budget it passed.

- `kit gen` (a write, and `-plan`) refuses (`INLINE_LOST`) and writes
  nothing; `-check` writes nothing and is not held, `-stubs` writes bodies
  that panic and is not held either.
- `kit check` gains the **`inline`** rule (`inline/lost`), declared by every
  design with a declaration (`decl: true`, `impl:`, `implements:`); a
  finding is exceptable by the function's id, like any symbol's.

Only a loss a **generated declaration** caused is the rule's. A third state,
built only when there is a loss — the base's generated files with the
result's hand-written ones — answers first: a function it does not inline
either lost its inlinability to a hand-written edit, which is the code's to
answer for. When that state does not compile — and a conversion always
renames the body to the impl in the same change, so it does not — the
result's own report answers: the function lost it to the generation when a
call the compiler inlined into it reaches, through the project's inlined
functions, one a changed generated file declares.

Nothing is measured when no generated production file differs from the
base's: `kit gen` over an unchanged design, `kit check` on a clean tree, cost
a `git diff`. A project in no repository has no base and is not measured. The
base's report is cached (`kit/inline/` in the user's cache, keyed by the
toolchain, the environment, the patterns and the digest of every production
Go file and module file of the base), and the go command replays a cached
compile's report, so a result that did not change compiles nothing.

### 2. `make from-zero`: the SDK rebuilt from the design, in a copy

`scripts/from-zero.sh` copies every file git tracks or would track, as the
working tree holds it, into a temporary directory — never touching the
working tree, so it runs over uncommitted work — and there:

1. deletes every file whose first line is kit's generated header (by header,
   never by name, as `regen` does) and kit's section of
   `tools/alloc-lane-targets.txt`;
2. runs `kit gen -stubs` and requires **every module of the census to
   compile** — `go build ./...` and `go vet ./...`, tests type-checked —
   from the design and the hand-written bodies alone, reporting per package
   the stubs written where an impl is missing;
3. runs plain `kit gen`, which removes the stubs, then `make api`, and fails
   on any file added, missing or changed against the working tree.

### 3. `make kit-coverage`: how much kit writes, counted

`kit design coverage` counts, per package, its exported declarations — each
function, method of an exported type, type, constant and variable — in a
file kit generated (`decl_gen.go`, `facade_gen.go`, `codes_gen.go`,
`design_gen.go`) and by hand, reading files only, every build constraint.
`make kit-coverage` prints the table, the total and the ten packages with
the most still written by hand. It is a report: it judges nothing, exits 0,
and is no CI gate.

`design/sdk.yaml` pins `project.kit.version: v0.1.0-rc.7`; the pin is no part
of the design's digest, but the generated headers carry the sha256 of the
file's bytes, so the ten files generated from `sdk.yaml` change their header.

### 4. Measured

- **The guard finds what was found by hand.** With `backoff`'s
  `NormalMultiplier` and `NormalJitter` made wrappers, `kit gen` refuses:
  `resilience.NewRetry` inlined at cost 76 and now costs 84 (+8),
  `backoff.Grow` 77 → 81 (+4) and `backoff.Widen` 80 → 84 (+4), each past
  the budget of 80. `Grow` and `Widen` were not in ADR 0168's account: the
  hand comparison saw `NewRetry` only.
- **On this change it finds nothing**: `kit check -base` [`fd54e5dd`](https://github.com/kitsunium/sdk/commit/fd54e5dd) (main,
  before `semver` and `clock` were converted) is green — no function of the
  SDK lost its inlinability to the stage-6 wrappers.
- **Cost** (M1 Pro, ≈ 320 packages, a 100 MB `-m=2` report): a clean tree
  adds nothing measurable to `kit check`'s 14 s; against a base with a
  change, the first check took 89 s (75 s of measurement) and the next one
  14.7 s, the base's report read from kit's cache and the result's replayed
  by the go command.
- **`make from-zero` is green**: 6 357 files copied, 563 generated files
  deleted and rebuilt, **0 stubs** — every impl the design names has its
  body, `semver`'s and `clock`'s included —, every one of the 17 modules
  compiles from the design and the bodies, and after `kit gen` and
  `make api` the copy is the working tree, byte for byte. Two minutes
  with bazel's caches warm, three and a half cold, most of it `make api`
  and bazel in the copy.
- **What kit writes** (`make kit-coverage`): of 7 208 exported declarations
  in 318 packages, **3 241 are generated (45.0 %)** — 1 749 re-exports, 1 374
  codes and sentinels, 106 ports, 12 declarations — and 3 967 written by
  hand. By layer: `pkg/v1` 89.8 % (1 767 of 1 967), `internal/core` 61.6 %,
  `internal/kernel` 11.1 %, `internal/service` 0 %, the framework 9.3 % (177 of 1 897).
  The most still by hand: `framework/model/internal/core` (388 of 390),
  `framework/model` (382), `framework/internal/kit` (325 of 400),
  `framework/kit` (277), `internal/core/net` (86 of 159),
  `internal/core/observe/trace` (82 of 116), `internal/kernel/errs` (71).

## Consequences / Semantics

- A wrapper that would make any function of the project stop inlining is
  never written in silence, whoever calls it; the conversion of a package's
  declarations can proceed package by package with the compiler as judge.
- "The SDK rebuilt by kit" has a number, and it is 45 %, not 100 %: the
  exported surface kit writes is the facades, the codes, the ports and two
  packages' declarations. The rest — every `internal/service` declaration,
  most of the framework — is still hand-declared, held equal to the design
  by the pins. The table says where the next conversion moves the most.
- `make from-zero` and `make kit-coverage` are local, like `make regen`:
  they need the pinned kit, which CI does not have.

## Breaking changes

None. Nothing exported changes: the change adds two local targets
(`make from-zero`, `make kit-coverage`) and a sourced helper, and kit gains a
rule (`inline`) and a refusal (`INLINE_LOST`) that hold what kit writes. A
generation that kit `v0.1.0-rc.6` wrote and that makes no function of the
project stop inlining is written by `v0.1.0-rc.7` byte for byte; one that
does is refused where it was accepted, which is the point.

## Deferred

- The guard compares inlinability, not cost: a function that still inlines
  at a higher cost, or a call site the inliner skips in a big function
  (cost past 80 inlined only into functions under 5 000 nodes), is not a
  finding.
- The evidence when the mixed state does not compile follows inlined calls;
  a loss reached through a non-inlined path — a hand-written edit in the
  same change growing a function that also inlines a wrapper — is laid to
  the generation.
- `make from-zero` builds the host cell; the twelve cells stay
  `cross-build`'s, on the committed tree.

## Alternatives considered

- **Compare the inline set by hand at each conversion**, as ADR 0168 did.
  Rejected: it found `NewRetry` and missed `Grow` and `Widen`, and a rule a
  person runs is a suggestion (principle 26).
- **Measure only what the design names** (ADR 0168's measurement alone).
  Rejected: a wrapper's cost lands on every function that inlines it,
  whoever calls it.
- **Rebuild from zero in the working tree**, as `make regen` does. Rejected
  for this target: it needs a clean tree and deletes files the developer may
  be editing; a copy proves the same over uncommitted work and writes
  nothing.
- **Hold the inline set in the SDK's CI.** Rejected for now: CI does not run
  kit (ADR 0163 §7), and the guard needs the base's build, which only kit
  knows how to state.

## References

- kitsunium/platform `feat/kit-inline-guard` — the guard, the `inline` rule,
  `kit design coverage`, kit `v0.1.0-rc.7`
- `scripts/from-zero.sh`, `scripts/kit-coverage.sh`, `scripts/kit-pinned.sh`
