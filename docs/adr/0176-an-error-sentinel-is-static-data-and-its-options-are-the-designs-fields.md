# ADR 0176 — an error sentinel is static data, and its options are the design's fields

- **Status**: Proposed
- **Date**: 2026-10-14
- **Deciders**: SDK maintainers (issues #273 and #276)
- **Depends on**: [ADR 0161](0161-an-untyped-error-fails-the-build.md) (every error is `errs.Define` or `errs.Wrap`), [ADR 0164](0164-an-error-code-is-the-designs-and-kit-writes-it.md) (codes and sentinels are the design's), [ADR 0168](0168-a-declaration-is-the-designs-and-a-body-is-the-codes.md) (a declaration is the design's, a body the code's)
- **Related**: [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a sibling, never a widening), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (the v0 licence), [ADR 0172](0172-a-product-initialises-what-it-imports-and-the-stores-stay-in-the-core.md) (§Deferred), issues #273 and #276

## Context

`kit gen` writes every sentinel of the SDK into a `codes_gen.go` from the
`sentinels:` blocks of `design/**` (ADR 0164). Measured on the tree at
`origin/main`: 679 `errs.Define(...)` calls in 93 generated files, from 95
`sentinels:` blocks.

`errs.Define(code, reason, public, private, opts ...DefineOption)` cannot be
initialised by the linker. It validates its arguments, can panic, and applies
variadic options that are closures. The compiler therefore emits a call at
run time and a package `init` for each package that holds one.

ADR 0172 §Deferred already measured the ceiling of the lever: a straight-line,
statically-initialisable constructor removed the init of 34 packages, which is
0.1 to 0.2 ms of the probe's 0.72 ms of initialisation. The 1.2 ms per redraw
that #276 attributes to start-up is mostly binary weight (pages mapped and
faulted), not init time. This ADR does not promise that figure; it makes the
sentinels data, which is a precondition of any further gain and is right on its
own: a sentinel is a constant record, and the language can say so.

The design's only options are two, both written as Go expressions that kit parses
(`GoSentinel.Options []string`, `parser.ParseExpr`) and never evaluates:

| Option | Uses in generated files | Argument |
|---|---|---|
| `errs.WithExitCode(x)` | 364 | an identifier such as `exitConfig` |
| `errs.WithHTTPStatus(x)` | 92 | an identifier or a literal |

The identifiers are redeclared per package, by hand, in `errors.go` files: 22
declarations `exit* int = N` of 12 names (`exitConfig = 78`, `exitUsage`,
`exitDataErr`, `exitIOErr`, `exitNoPerm`, ...). The same number is therefore
written in many places and kit cannot check it. Because the option is an
arbitrary expression, kit cannot write a sentinel as a constant.

## Decision

1. **A sentinel's exit status and HTTP status are fields of the design.** A
   `sentinels:` entry gains `exit:` and `http:`.
   - `exit: CONFIG|78` is either a number in 0..255 or a sysexits name:
     `USAGE`=64, `DATAERR`=65, `NOINPUT`=66, `NOUSER`=67, `UNAVAILABLE`=69,
     `SOFTWARE`=70, `OSERR`=71, `CANTCREAT`=73, `IOERR`=74, `TEMPFAIL`=75,
     `PROTOCOL`=76, `NOPERM`=77, `CONFIG`=78.
   - `http: 401` is a number in 100..599.
   - Absent means the defaults of `errs` (exit 70, HTTP 500).
   - `options:` stays accepted for any other expression, but is refused beside
     `exit:` or `http:` on the same sentinel. A sentinel that keeps `options:` is
     written with `Define`: dynamic by choice.

2. **A sentinel without `options:` is written with `errs.Static`.**
   `X = errs.Static(CodeX, "REASON", "public", "private", http, exit)`.
   `errs.Static` is a sibling of `Define` (ADR 0039), not a change of it: it takes
   constants only, its body is one composite literal, and it has no panic, no
   variadic and no closure, so it inlines and the variable initialises at link
   time. The final name is fixed in PR 1.

3. **Validation moves to `kit gen`.** Today `Define` validates at init and
   panics. For `Static` kit checks, when it generates: the reason's pattern, the
   public text (at most 120 runes, no newline), the code's layer, and the exit and
   HTTP ranges. A sentinel the design could not state does not reach the code.

4. **The registry audits learn `errs.Static`.** `registry_external_test.go` and
   `scripts/pre-commit/check-audit-coverage.sh` look for `errs.Define`; an audit
   that knows only `Define` would not see a `Static` sentinel, which is the gap
   ADR 0020 records. They are extended in the same change that introduces
   `Static`.

5. **kit changes in `kitsunium/platform`**, listed here for the record (read, not
   verified against that repository by this ADR): `design/codes.go` (the `Exit`
   and `HTTP` fields, their validator, the twin of `DefineSpelled`),
   `design/library.go` and `design/project.go` (a `codes.static` setting beside
   `codes.define`, and the JSON schema), `projecttype/library/gencodes.go`
   (`sentinelDecl`, `codesImports`), `importcodes.go` (around line 208, evaluate
   constant options into `exit:` and `http:`), `codes.go` (the `codes` rule
   accepts either call), `gen`, `gencanon`, `gendocs`, `facts` and `importer`,
   the goldens under `testdata/codes/**`, the docs (`docs/model.md`,
   `design/CLAUDE.md`, `projecttype/library/CLAUDE.md`, `CLAUDE.md`),
   `cmd/kit/main.go` and `scripts/rc/**` (stamp rc.9, the counting oracles).

## Plan

| PR | Repository | Content |
|---|---|---|
| 0 | sdk | This ADR (docs only, `release:patch`). |
| 1 | sdk, after acceptance | Add `errs.Static` design-first: `design/kernel/errs.yaml`, `kit gen` (rc.8 suffices), `make api`, `make docs-readme`, and the audits of Decision 4. **Gate**: `go build -gcflags=-m` shows `Static` inlined, and `go tool nm` shows a probe sentinel in static data with no init. If it does not, this ADR is amended before anything else moves. Adds a symbol to `pkg/v1/errs`, so a minor for that module. |
| 2 | platform, after PR 1 is merged | kit rc.9: `exit:` and `http:`, `codes.static`, the generator, the importer, the goldens, both forms accepted; then the tag. |
| 3 | sdk, after rc.9 is tagged | Pin `design/sdk.yaml` `project.kit.version` to rc.9, set `codes.static`, rewrite every `options:` as `exit:` and `http:` (`kit design import`, or a reviewed script), `kit gen`, `make api`, `make docs-readme`, delete the `exit*` and `http*` constants that nothing reads. Proof: `make regen`, `make from-zero`, `make test`, `make test-alloc`, `make lint`. May be split by layer. |
| 4 | sdk | Measure `framework/kit/testdata/cliprobe` (inittrace, size, page faults) into `framework/kit/BENCH.md`, and report on #276. |
| 5 | optional | Remove `options:` in kit rc.10. |

## Consequences

- **Compatibility.** `Static` is a sibling of `Define` (ADR 0039): nothing
  published moves, the observable values of every sentinel and `errors.Is` are
  unchanged. The change of the design's format is covered by the v0 licence of
  ADR 0040. kit refuses a design whose pin names another kit, so the SDK moves to
  rc.9 in one PR (PR 3).
- **The exit and HTTP numbers are written once**, in the design, and kit checks
  their range; the per-package `exit*` constants disappear.
- **The gain is capped.** About 0.1 to 0.2 ms of init (ADR 0172); the rest of
  #276 is binary weight and is outside this ADR.

## Risks

- The compiler may not initialise statically under `-race`, `-N`, coverage or
  GOARCH=386. The gain is then smaller; behaviour does not change.
- Validation moves from `init` to kit plus the audits. If an audit knows only
  `Define`, a `Static` sentinel is unchecked (the gap of ADR 0020): Decision 4.
- A large regenerated diff (93 files).
- kit is not public, and the SDK's CI runs without it; `make api-check` is CI's
  hold on the generated files, and `make regen` and `make from-zero` are local.
- The gain is capped, as stated above.

## Verified and not yet proved

Verified by reading and counting in this repository: the 679 calls, the 93
files, the 95 blocks, the 364 and 92 option uses, the 22 `exit*` declarations of
12 names, and the signature and body of `Define`. Taken from the analyst and
from ADR 0172, not re-measured here: the 0.1 to 0.2 ms ceiling, and the shape of
kit's code in Decision 5.

To be proved in PR 1: that `errs.Static` is initialised statically by the
toolchain pinned in `MODULE.bazel`, and that `docs/api` is byte-identical for
every sentinel after the rewrite.

## Alternatives considered

- **Keep `Define` and only reduce its cost.** Rejected: its options are closures
  and it can panic, so it stays a call at run time.
- **Make `Define` static by changing its signature.** Rejected: widening a
  published port is what ADR 0039 forbids; a sibling costs nothing.
- **Leave the options as Go expressions and evaluate them in kit.** Rejected: it
  keeps the numbers outside the design, and kit would have to evaluate arbitrary
  expressions to prove a constant.

## References

- `internal/kernel/errs/error.go` (`Define`), `design/kernel/errs.yaml`
- ADR 0172 §Deferred, ADR 0020, ADR 0035
- Issues #273, #276
