# ADR 0166 — a facade is the design's, and a forwarder's form is measured

- **Status**: Accepted
- **Date**: 2026-10-05
- **Deciders**: SDK maintainers (the owner's decision: stage 3 of "kit regenerates the Go SDK from its design")
- **Amends**: [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md) §1 (doc text is recorded for ports alone), §2 (what kit writes) and §10 (the one function variable is an exception); [ADR 0164](0164-an-error-code-is-the-designs-and-kit-writes-it.md) §2 (the production code kit writes now holds functions, not only declarations and the calls that initialise them)
- **Related**: [ADR 0165](0165-a-performance-contract-is-the-designs-kit-measures-what-the-compiler-decides-and-a-test-holds-the-rest.md) (a budget is a function's performance contract; an `inline` budget on a forwarder decides its form here), [ADR 0008](0008-readme-from-code-generation.md) (READMEs stay gomarkdoc's), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (what a `pkg/v1` alias may point at), [ADR 0138](0138-a-doc-link-resolves-or-it-is-not-written.md), [ADR 0155](0155-every-layer-groups-its-packages-by-family-and-a-path-may-move-while-v0.md)

## Context

ADR 0163 made `design/` the law of the exported surface and ADR 0164 moved
the error codes into it. `pkg/v1` stayed hand-written: 122 files, 19 625
lines, almost all of them one of four shapes over an internal package — a
type alias, a re-exported constant or variable, and a forwarder whose body
is one call of another package's function with its own parameters. Each was
therefore written twice, once in the design (its name and signature) and
once in the code (its name, signature, doc comment and body), and its doc
comment lived in the code alone.

ADR 0163 §10 also kept one function variable, `var ReasonOf =
kerrs.ReasonOf`, as an `exceptions:` entry with its numbers: its forwarder
does not inline (cost 88 against the inliner's 80) and measured slower than
the variable. The owner asked that this become a derived verdict, measured,
rather than an exception.

## Decision

### 1. A package's re-exports are a section of its design file

Each package of the design gains `facade:` (kit's library dialect, platform
ADR 0010's amendment of 2026-10-05, [kitsunium/platform#57](https://github.com/kitsunium/platform/pull/57)):
the declarations it re-exports of other packages, in order. A declaration is
one of

- `alias:` — a type declaration of aliases: each `name`, `target`, type
  parameters;
- `const:` / `var:` — a declaration whose every spec names another package's
  constant or variable: `reexport:` (`corelock.LockNotHeld`), `typed: true`
  when it is written with its type (`MaskByMajor Code = kerrs.MaskByMajor`),
  and the constant's type and exact value or the variable's type, which the
  pins hold;
- `func:` — a forwarder: its signature, every parameter named, and
  `forward:`, the function it calls as the package spells it, type
  arguments included (`events.On[E]`).

Every entry carries its `doc`, line `comment`, `note` (a comment standing
apart above it) and `blank` (a blank line before it in a block); a
declaration is a block when it holds several entries or says `block: true`,
exactly as the code wrote it, so a block's doc still documents its members
and go/doc prints the block as it did. This amends ADR 0163 §1: the doc text
of every re-export is the design's, as a port's and a code's already were.

What is not a re-export stays hand-written: a function with a body of its
own (89 — `errs.HasAnyCode`, the logger's `Info` and its siblings, the codec
verbs), a wrapper whose types are not its callee's (`errs.New` and
`errs.Wrap` return `error` over the kernel's `*errs.Error`, `redact.New` a
`Redactor` over the engine's concrete type: a variable bound to the callee
would publish the callee's type), a constant or variable of the package's
own (91 constants, 8 variables), a block that mixes a re-export with one,
the package comments, and every file's `//go:generate gomarkdoc` line.

### 2. kit writes them into `facade_gen.go`

`kit gen` writes each package's `facade_gen.go`: its declarations in the
design's order, with every comment, under the kit header and its two digests
(ADR 0163 §2), which `make api-check` verifies. A forwarder's body is kit's,
`return callee(params…)`; the `//:` comments the hand-written bodies carried
for ktn-linter are not kept, since ktn-linter skips a generated file. This
amends ADR 0163 §2 and ADR 0164 §2: kit's production code now holds
functions. They are the same functions with the same bodies, so a program
compiles to the same code — measured below.

### 3. A forwarder's form is a measurement, under a budget the design names

A forwarder is a function. One the project's budgets give an inline budget
(`budgets:`, `inline: true`) is a function only while the compiler inlines
it, and otherwise a variable bound to its callee. kit decides which by
measuring, not by reading a declaration: it writes the forwarder as a
function, builds its package with `go build -overlay … -gcflags=<package>=-m=2`
in the platforms' first cell (`linux/amd64`, so every host writes the same
bytes), under the loader's pinned environment, and reads the compiler's own
report. `can inline F with cost N` keeps the function; `cannot inline F:
<why>` writes `var F = callee` under the note

```
// kit measured: ReasonOf's forwarder does not inline on linux/amd64 (function too complex: cost 88 exceeds budget 80), so kit writes a variable.
```

and its pin's marker says `var`, as `docs/api` does. A generic forwarder is
always a function. kit check holds the verdict both ways: the facade rule
refuses a function variable that no budget allows, or whose forwarder now
inlines; the budgets rule refuses a budgeted forwarder written as a function
that no longer inlines.

The `exceptions:` entry for `errs.ReasonOf` becomes such a budget in
`design/sdk.yaml`, its numbers kept in the comment, and the verdict is
derived: its forwarder costs 88, so kit writes `var ReasonOf =
kerrs.ReasonOf`; the day the kernel accessor inlines within the budget,
`kit gen` writes the forwarder with no design edit — what §10's note asked
for ("until the kernel accessor returns from one place"). This amends ADR
0163 §10, and `design/sdk.yaml` has no `exceptions:` left.

**Why a budget, and not the cost alone.** Measured on `linux/amd64`, 19 of
the 339 non-generic forwarders written as functions do not inline —
`semver.PseudoVersionRev`, `secret.SubjectOf`, `sql.NewTransactor` and
`sql.NewChecker` (91), `id.NewTypeID` (90), `group.NewJoined` (89),
`errs.CodeOf` (88), `profiling.CaptureCPU` (87), `crypto.WrapKey` and
`crypto.UnwrapKey` (86), `cgroup.Create` (85), `scheduler.Parse` (84),
`logger.LogAttrs` and `codec.MultipartContentType` (83),
`spool.AttemptFrom` and `profiling.Goroutines` (82), and the three
`DrainSignal` (81) —, and `errs.CodeOf` costs exactly what `errs.ReasonOf`
costs. ADR 0163 §10 kept `CodeOf` a function on its benchmark (never beyond
the 3 % at p < 0.05) and `ReasonOf` a variable on its own. The cost alone
would turn the nineteen into variables a consumer could reassign — the shape
the facade rule exists to refuse — and change the exported kind of nineteen
symbols. So the design names the forwarder whose form follows the inliner,
and the measurement decides its form: a rule the cost cannot express is not
smuggled into a threshold. The other 320 inline, every one; the 37 generic
forwarders are inlined per instantiation.

### 4. The move

`kit design import -type library -facade` (kit `v0.1.0-rc.4`) read the
re-exports of every package of the `public` layer into `facade:`; the
hand-written declarations were then moved out — content moved, never
deleted — and `kit gen` wrote them back:

- 1 169 declarations moved out of 83 packages: 480 aliases, 450 constants,
  442 variables and 377 forwarders;
- 14 files left with nothing but a package comment that was one fragment of
  several are gone (rule 5), each comment joined to its neighbour's in
  file-name order — the order go/doc joins them in — so every package
  comment reads as it did (`codec/multipart.go`, `server/http_options.go`,
  five of the logger's, two of `process`'s, `secret/subjectkeys.go`, four of
  `token`'s); 46 files that hold their package's documentation stay, holding
  it alone, as `lock.go` did when its ports moved (ADR 0163 §2);
- 83 `facade_gen.go` are kit's, and every pin (`api_gen*_test.go`) has the
  body it had — only its header's design-file digest changed —, so each
  re-export is still held by its type; `ReasonOf`'s marker still says `var`.

`docs/api` changes only where a symbol's file changed (1 758 records now name
`facade_gen.go`) — every doc text, value and canonical signature is the
same — with one exception, nine `spelled` fields: `pkg/v1/app/cli`'s
sentinels, whose type `cli.go` spelled `*kerrs.Error` because it imported
the kernel's errs under that name for `Status`, and `facade_gen.go`, which
does not import it, spells `*errs.Error`. Their canonical type is unchanged.
The 71 regenerated READMEs differ only in their source links (file and line);
`go doc` reads every package as it did.

### 5. Measured: the same program

- A consumer importing every `pkg/v1` package (87) and calling
  `lock.NewMemory`, `errs.New`, `errs.ReasonOf`, `errs.CodeOf`,
  `errs.PublicOf` and `errs.HasCode`, built `-trimpath` from
  [`d7340edf`](https://github.com/kitsunium/sdk/commit/d7340edf) and from this
  change: the same 9 052 symbols with the same kinds, and the same sizes but
  for the line tables (`runtime.pclntab` +48 bytes, `runtime.epclntab` +16:
  positions, not code), the build information (the replace path) and two
  read-only generic dictionaries of `crypto/internal/fips140` whose sizes, as
  `nm` estimates them from addresses, swapped (48 and 16); 8 481 522 bytes
  both.
- The machine code is the same: the 563 SDK functions of that binary have
  the same instructions, compared function by function with addresses,
  PC-relative offsets and the global-data displacements masked; so do the
  loops of the forwarder benchmarks below, which inline the forwarders.
- `benchstat` over the forwarder benchmarks — `pkg/v1/errs`'s 18
  `Facade_*`, `pkg/v1/observe/trace`'s 4 and `pkg/v1/app/lock`'s 6
  `Memory_*` —, ten interleaved runs of each side on an M1 Pro shared with
  other jobs: 27 of the 28 `~`, and `trace`'s `Facade_String` +1.50 %
  (±3 %, p = 0.034), within the 3 % a forwarder may cost; geomean +0.25 %
  (errs), +0.75 % (trace), +1.02 % (lock). Allocations equal to the unit.

### 6. How a re-export changes now

An alias, a re-exported value, a forwarder or one of their doc comments
changes in the domain's design file, then `kit gen`; a forwarder whose form
should follow the inliner gets an inline budget. `make api-check` fails a
`facade_gen.go` edited by hand, or a design edited without `kit gen`, on the
digests. The SDK's CI still runs no kit (ADR 0163 §7); `kit gen -check`
re-measures every budgeted forwarder.

## As built

- `design/`: 58 surface files changed, the 83 packages' re-exports under
  `facade:`, and `sdk.yaml` (the budget, `kit.version: v0.1.0-rc.4`);
  `kit design import -type library` over the tree writes nothing; `kit check`
  gives no finding under any of its eight rules (`exceptions` has nothing to
  check); `kit gen -check` is clean.
- `pkg/v1`: 83 `facade_gen.go` written, 14 files removed, 92 hand-written
  files trimmed to their own declarations or their package comment (or
  holding a neighbour's joined package comment).
- Cost of kit's measurement: one `go build` of `pkg/v1/errs` with the
  compiler's report, inside `kit gen`'s 8 s on the SDK.

## Consequences / Semantics

- A re-export is written once, in the design; its declaration, its doc
  comment and its pin are written from it.
- A doc edit on a re-export is a design edit and needs `kit gen`, like a
  port's or a code's (ADR 0163 §4's exception, extended once more); a doc
  edit on a package comment or on a hand-written declaration still does not.
- The exported surface is identical — same names, kinds, values, signatures
  and doc texts — so the release is a patch.
- Three package-comment sentences now name a file that no longer holds what
  they say: `token`'s "the codes are re-exported in codes.go", and `errs`'s
  "introspection (this file)" in `accessors.go` and "accessors.go re-exports
  the read-only introspection surface" in `construct.go` — the declarations
  are in `facade_gen.go`. Every package comment is kept byte for byte here
  (§4: `docs/api`'s package records and the READMEs unchanged but for their
  links), so correcting them is a doc edit of its own, which needs no kit.

## Breaking changes

None. Every exported name, kind, value, type and doc comment is the one the
code declared; `ReasonOf` is still a variable and every other forwarder still
a function. A contributor now needs kit to change a re-export or its doc
comment — the precondition ADR 0163 set for the rest of the surface.

## Deferred

- The nineteen forwarders that do not inline stay functions; giving one an
  inline budget is a measured decision of its own (ADR 0163 §10's
  benchmark), not a consequence of this record.
- `kit` measures on the platforms' first cell only; a forwarder whose cost
  differs per architecture (an intrinsic) would be judged on `linux/amd64`.

## Alternatives considered

- **Decide the form by the cost alone** (var whenever the forwarder does not
  inline). Rejected: nineteen exported functions would become variables, the
  symbol set of every consumer would change, and `CodeOf` and `ReasonOf` —
  equal in cost — could not be told apart.
- **Keep `ReasonOf` in `exceptions:`.** Rejected: the owner asked for a
  derived verdict; an exception records a decision, a budget records the
  rule that decides it.
- **Keep the forwarders' `//:` body comments.** Rejected: they exist for
  ktn-linter's rules on hand-written bodies, and a generated body is kit's.

## References

- [kitsunium/platform#57](https://github.com/kitsunium/platform/pull/57) — the dialect, `kit gen`'s facade, the measured verdict and the import (kit `v0.1.0-rc.4`)
- `pkg/v1/CLAUDE.md`, and each facade package's `CLAUDE.md` §Generated
