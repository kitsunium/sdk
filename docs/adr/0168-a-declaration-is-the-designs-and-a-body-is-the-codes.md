# ADR 0168 — a declaration is the design's, and a body is the code's

- **Status**: Accepted
- **Date**: 2026-10-05
- **Deciders**: SDK maintainers (the owner's decision: stage 6 of "kit regenerates the Go SDK from its design" — "reprendre le SDK de 0 avec kit … kit l'outil qui génère la doc et le code (théorique) qui permet de générer le code le plus performant possible")
- **Amends**: [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md) §2 (what kit writes: type declarations and the functions that wrap a hand-written body, besides ports, codes and facades)
- **Related**: [ADR 0165](0165-a-performance-contract-is-the-designs-kit-measures-what-the-compiler-decides-and-a-test-holds-the-rest.md) (an inline budget is measured, never assumed), [ADR 0166](0166-a-facade-is-the-designs-and-a-forwarders-form-is-measured.md) (a forwarder's form is measured), [ADR 0167](0167-a-doc-comment-is-the-designs-and-a-readme-is-written-from-docs-api.md) (every doc is the design's), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md), [ADR 0090](0090-a-port-named-in-public-must-be-implementable-in-public.md) (`clock`'s public contract)

## Context

After stage 4 the design held every exported symbol's signature, its doc
comment, the ports, the codes and the facades, and kit wrote all of them but
two things: a type's declaration and a function's. Rebuilding a package from
the design still meant writing, by hand, every `type T struct { … }` and
every `func F(…) … {` line the design already described, and keeping them
equal to it through the pins. The owner's goal is a package rebuilt from
`design/` and its function **bodies** alone.

What makes a declaration movable is that a function's body can live under
another name: `func F(a A) R { return f(a) }` — a wrapper — calls the
hand-written `f`. The wrapper is only acceptable if it costs nothing: the
SDK's rule since ADR 0163 §10 is that a public function is never a call more
than it was.

## Decision

### 1. Three members of the design (kit `v0.1.0-rc.6`, platform ADR 0010's amendment of 2026-10-05)

- **`decl: true`** on a defined type: kit writes its declaration. A struct
  then lists every field, unexported ones included, in order — the struct's
  layout is the design's. The pins, the markers, the doc digests and the
  api rule read its exported view, so `docs/api` is unchanged.
- **`impl:`** on a function or a method: kit writes the wrapper, one call of
  the unexported body with the parameters in order; a method calls its
  type's unexported method.
- **`implements:`** on a type: kit writes `var _ I = (*T)(nil)` — or the
  type's own zero when every method has a value receiver — replacing a
  hand-written compliance line.

kit gen writes them into each package's `decl_gen.go`, with the design's doc
comments, under the kit header (ADR 0163 §2), which `make api-check`
verifies.

### 2. A wrapper costs nothing, or it is not written

Every wrapper is `inline: true` implicitly, and kit measures it from the
compiler's own report (`-gcflags=<pkg>=-m=2`, `linux/amd64`, ADR 0165):

- the wrapper must inline — or every call pays one more;
- its **impl must inline into it** — at a direct call a wrapper over an impl
  that does not inline costs what the function did, but through a func value
  (`slices.SortFunc(list, semver.Compare)`), a method value or an interface,
  the wrapper itself runs and calls its impl: one call more. With the impl
  inlined into it, the wrapper's code is the function's as it was;
- every facade forwarder of the wrapper (`pkg/v1`) must still inline — the
  wrapper's cost is inlined into it.

kit gen refuses a wrapper that fails any of the three
(`DECL_NOT_INLINED`); kit check's budgets rule holds the code to the same. A
generic wrapper is refused by the design: the compiler inlines it per
instantiation, so nothing can be measured.

### 3. What moved, and what did not

- **`kernel/semver`**: `IsValid`, `Prerelease` and `IsPseudoVersion` are
  wrappers (`isValid`, `prerelease`, `isPseudoVersion`; their impls cost 67,
  68 and 67 and inline into them, the wrappers 71, 72 and 71, their
  `pkg/v1/data/semver` forwarders 75, 76 and 75). Three keep their bodies:
  `Compare` (its body costs 511: through a func value — the documented
  `slices.SortFunc(list, Compare)` — a wrapper would be a call more),
  `PseudoVersionRev` (78: its wrapper would cost past 80) and
  `PseudoVersionTime` (its wrapper inlines at 70, but its forwarder would go
  from 70 to 83 and stop inlining).
- **`kernel/clock`**: `Clock`, `Timed`, `Waiter`, `Timer`, `Ticker` and
  `ManualClock` are declared by kit (`clock.go`, `timer.go` and `ticker.go`,
  left empty, are gone — rule 5); `NewManualClock`, `ManualClock.NewTimer`
  and `ManualClock.Sleep` are wrappers; `ManualClock`'s three assertions are
  its `implements:`. The other eight methods keep their bodies: each takes a
  lock with a `defer` and does not inline, and `ManualClock` is reached
  through `Clock` and `Timed`, where a wrapper over them would be a call more
  — measured first as a dry run, which is how §2's second clause was found.
- **`kernel/backoff`** is not converted. `Value.Delay`'s body (128) does not
  inline; `Grow` (77) and `Widen` (80) leave no room for a wrapper;
  `NormalMultiplier` and `NormalJitter` inline as wrappers, but at four more
  each they pushed `service/app/resilience.NewRetry` past the inliner's
  budget — found by comparing the whole module's `-m` report, which kit does
  not do (§Deferred).

### 4. Measured: the same program

- `kit design import -type library -decls internal/kernel/semver -decls
  internal/kernel/clock` read the two packages, their wrappers
  hand-shaped first; `docs/api` changes only 24 `file` fields
  (`decl_gen.go`); every doc text, value and canonical signature is the same,
  and the READMEs differ only in their source links.
- The set of functions the compiler can inline, over `internal/…`, `pkg/…`
  and `framework/…` on `linux/amd64`: no function left it; it gained the six
  impls.
- A consumer importing every `pkg/v1` package and calling the semver
  functions, `ManualClock` and `Backoff.Delay`, built `-trimpath` from
  [`fd54e5dd`](https://github.com/kitsunium/sdk/commit/fd54e5dd) and from this change: the same symbols with the same sizes, but
  `go:func.*` (+64 bytes, the inlining tree of one more level) and the module
  information (the replace path); the machine code of all 576 SDK functions
  is the same, compared function by function with addresses and global-data
  displacements masked; `main.main` keeps its size and its instructions but
  for four inline-mark `NOPL`s, one per inlined wrapper.
- `benchstat`, twelve interleaved runs of `internal/kernel/semver`'s seven and
  `internal/kernel/clock`'s six benchmarks on an M1 Pro whose load average
  was 14 to 25: every one `~` but `CompareReleases` (−6.8 %, p = 0.033, a
  function this change does not touch); geomean −1.6 % (semver) and +3.7 %
  (clock, ±18 % to ±107 %). With the machine code identical, the numbers
  measure the machine's load.

## Consequences / Semantics

- A package can be rebuilt from its design: `kit gen -stubs` writes a
  panicking body for each impl the code lacks (`stubs_gen.go`, never
  committed: kit check and `kit gen -check` refuse it), the package compiles,
  and the bodies make its tests pass.
- A type's declaration, a wrapper and an assertion are design edits that
  need kit, as a port's are; a body is the code's.
- An impl is an unexported function with a doc comment of its own
  (ktn-linter's rule), naming the wrapper it is the body of.
- The exported surface is identical, so the release is a patch.

## Breaking changes

None. Every exported name, kind, type, signature, value and doc text is the
one the code declared: `docs/api` differs only in 24 `file` fields, which
name `decl_gen.go`, and a consumer's binary has the same symbols and the same
machine code (§4). The exported type `ManualClock` keeps its name, although
`internal/`'s role-suffix rule (`KTN-STRUCT-ROLE`) would name a new struct
otherwise: it is published as `pkg/v1/clock.ManualClock` (ADR 0090), and a
rename is a break this change does not make. ktn-linter skips a generated
file (`skip_generated: true`), so moving the declaration into `decl_gen.go`
raises nothing it did not raise before.

## Deferred

- kit measures a wrapper, its impl and the facade forwarders of it, not
  every function of the project that inlines it: `backoff`'s normalisers
  were caught by comparing the whole module's inline report by hand. A kit
  measurement of the project's inline set before and after is the next step.
- `kernel/backoff`, and every package whose bodies do not inline, keep their
  declarations hand-written, held by the pins as before.
- The `implements:` assertions are written in `decl_gen.go`, beside the
  declaration kit writes, and not in a `*_compliance.go` file as
  `internal/`'s rule (`KTN-IFACE-ASSERT-PLACEMENT`) asks of hand-written ones:
  they are compile-time declarations that cost nothing at run time, the
  rule's gate skips generated files, and kit writing a second file per
  package for them is a change of its own.

## Alternatives considered

- **Generate the bodies too** (a body fragment kit pastes into the
  declaration). Rejected: a body is the code's, written and reviewed as Go;
  kit would become an editor of logic, and nothing it could measure would
  hold the fragment to its tests.
- **Write a wrapper whatever it costs, and report the cost.** Rejected: a
  public function never costs a call more than it did (ADR 0163 §10, ADR
  0165), so a wrapper that does not inline — or whose body does not inline
  into it — is refused, and the function keeps its body.
- **Point every caller at the impl** (so a wrapper's cost never reaches
  them, as tried on `backoff`'s `Grow` and `Widen`). Rejected for this
  change: the callers outside the package cannot see an unexported impl —
  `resilience.NewRetry` is one —, so the wrapper's cost reaches them all the
  same; the package stays hand-written instead.


## References

- kitsunium/platform `feat/kit-decls-from-design` — the dialect, `kit gen`'s
  declarations and stubs, the measurements, the import (kit `v0.1.0-rc.6`)
- `internal/kernel/semver/CLAUDE.md`, `internal/kernel/clock/CLAUDE.md`
