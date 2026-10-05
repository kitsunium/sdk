# ADR 0164 — an error code is the design's, and kit writes it

- **Status**: Accepted
- **Date**: 2026-10-05
- **Deciders**: SDK maintainers (the owner's decision: stage 2 of "kit regenerates the Go SDK from its design")
- **Amends**: [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md) §2 ("nothing else in production code is generated") and §8 (`codeRangeOwners` hand-kept); [ADR 0035](0035-pp-range-ownership-enforcement.md) (how the table stays independent of the constants)
- **Related**: [ADR 0005](0005-sdk-error-codes-dotted-quad.md), [ADR 0006](0006-sdk-error-code-registry-extension.md), [ADR 0020](0020-errs-audit-dual-reason-derivation.md) (the two Reason derivations), [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md) (where a code is declared)

## Context

ADR 0163 made `design/` the law of the exported surface and had kit write the
pins and the ports; the 739 error codes stayed hand-written — 733 `Code…`
constants and 641 `errs.Define` sentinels, in a `codes.go` and an `errors.go`
per package, each code mirrored in the design as a bare constant with its
value — and the range table, `codeRangeOwners`, stayed a literal in the
ownership audit's test file. A code was therefore written three times (the
constant, the design's constant, the sentinel's Define), and its doc comment,
Reason and texts lived in the code alone.

## Decision

### 1. A package's codes and sentinels are sections of its design file

Each package of the design gains two sections (kit's library dialect, platform
ADR 0010's amendment of 2026-10-05):

- `codes:` — the const declarations of the code type (`codes.type`,
  `errs.Code`) the package allocates, in order. A declaration holds its codes,
  the `type` the package spells (`errs.Code`, or `v1errs.Code` where the
  package imports two `errs`), and, for a block, its doc; each code its
  `name`, its exact `value` as written (`"0x00_02_15_01"`), its `doc`, its line
  `comment` (absent when it is the dotted quad, which kit then writes), and a
  `note` — a comment standing apart above it, such as a range's
  `range: 0.2.21.0 - 0.2.21.255` header.
- `sentinels:` — the var declarations of the errors made with `codes.define`
  (`errs.Define`): each sentinel's `name`, its `code` (a code of the same
  package), `reason`, `public`, `private`, its `options` as the Go expressions
  the package spells (`errs.WithExitCode(exitConfig)`), `doc`, `comment` and
  `note`.

A declaration is a block — `const ( … )`, `var ( … )` — when it holds several
entries or says `block: true`, exactly as the code wrote it, so a block's doc
still documents its members. Every exported name, value, Reason and text is
the hand-written one: the Reasons are recorded, not derived, and still satisfy
one of ADR 0020's two derivations, which the AST audit keeps checking on the
generated files (`//:audit_sources` globs `*.go`).

### 2. kit writes them into `codes_gen.go` — production code that runs

`kit gen` writes each package's `codes_gen.go`: its codes, then its sentinels,
in the design's order, with every comment, under the kit header and its two
digests (ADR 0163 §2), which `make api-check` verifies (genindex judges any
file with a kit header). This amends ADR 0163 §2: the sentinels' `Define`
calls run at package initialisation. They are the same calls with the same
arguments, so the program does the same work — measured below.

The constants the unexported options name (`exitConfig`, `httpBadRequest`, …)
and every function stay hand-written in `errors.go`, which keeps them. The
six meta-codes of `internal/kernel/errs` (`0.0.0.1`–`0.0.0.6`, `iota`, layer 0)
are no allocation and stay hand-written in `codes.go`.

### 3. The ranges' table is a separate block, and stays independent

`design/sdk.yaml`'s `codes:` section gains `ranges:` — each range `MM.LL.PP`,
the directory that owns it, and the table's line comment — and `codes.owners`
names `internal/kernel/errs/codes_gen_test.go#codeRangeOwners`, which kit
writes from it, in `errs_test`. ADR 0035's requirement is that the table be
independent of the constants it audits: a table derived from the constants
records any squatter as the rightful owner. It still is. The codes are written
from each package's `codes:` in its domain's file, the table from a separate
block of the project file that kit never derives from them (the import does
not write it either: it was copied once from the hand-kept table, and is kept
byte for byte by every later import). The ownership audit is unchanged: it
still compares the code's constants, read from the tree, with the table — two
outputs of two blocks authored apart, which can disagree exactly as before.
kit's `codes` rule reads the same table, and its design validation refuses a
range owned twice. `check-core-symmetry.sh` reads the table where it now is.

### 4. The move

`kit design import -type library -codes` read the codes and sentinels from the
code into the design; the hand-written blocks were then moved out — content
moved, never deleted — and `kit gen` wrote them back:

- 751 declarations moved out of 98 packages, and the codes or sentinels of
  four blocks that keep a hand-written constant or variable beside them (the
  three database connectors' `CodeURLMalformed`, and `third-party/transform`'s
  sentinels beside its wrap parameters);
- 132 files left with nothing but their package comment are gone (rule 5);
  each comment joined its neighbour's in file-name order, the order go/doc
  joins them in — 81 into the next hand-written file, and 48, in the 24
  packages that kept no hand-written file, into a `doc.go` — so every
  package comment reads as it did;
- 98 `codes_gen.go` and the ranges' `codes_gen_test.go` are kit's.

`docs/api` changes only where a symbol's file changed (1 374 records now name
`codes_gen.go`) — every doc text, value and canonical signature is the same —
with one exception, four `spelled` fields: `pkg/v1/data/codec`'s four codes
are spelled `v1errs.Code`, because their `codes_gen.go` also holds the
sentinels, which `Define` through the kernel's `errs`, and one file cannot
name both packages `errs`. Their canonical type is unchanged.

### 5. Measured: initialisation does the same work

A binary importing every public package of the SDK (98 packages, `-trimpath`),
built from `a4d46d28` and from this change, run alternately with
`GODEBUG=inittrace=1`, the SDK's packages summed per run, two rounds of 400
runs each:

| | before (`a4d46d28`) | after |
|---|---|---|
| init clock, median (round 1 / round 2) | 0.5810 / 0.5505 ms | 0.5815 / 0.5430 ms |
| init bytes | 230 032 | 230 032 |
| init allocations | 1 681 | 1 681 |

The clock is noise (p10 0.427 / 0.423 ms before, 0.432 / 0.414 ms after);
the bytes and allocations are equal to the unit, and `go tool nm` lists the
same symbols in both binaries.

### 6. How a code changes now

An error code, a sentinel, its doc or its texts change in the domain's design
file, then `kit gen`; a new range is added to `codes.ranges` in the same
change. `make api-check` fails a `codes_gen.go` edited by hand, or a design
edited without `kit gen`, on the digests. The SDK's CI still runs no kit
(ADR 0163 §7).

## Consequences

- A code is written once, in the design; its constant, its sentinel and its
  pin are written from it.
- A doc edit on a code or a sentinel is a design edit and needs `kit gen`, like
  a port's (ADR 0163 §4's exception for ports, extended to codes).
- The exported surface is identical — same names, kinds, values and
  signatures — so the release is a patch.

## Alternatives considered

- **Keep `codeRangeOwners` hand-written in the test file.** Rejected: the
  owner asked for the table in the design, and the independence ADR 0035
  needs is between the table and the codes, not between the table and the
  design — two separate blocks keep it.
- **Generate the table from the codes.** Rejected for ADR 0035's reason: it
  would agree with any squatter by construction.
- **Have the audit read `design/sdk.yaml`.** Rejected: the SDK's CI reads the
  design as bytes only (ADR 0163 §3), and the kernel's test would need a YAML
  parser the kernel cannot import.
- **Flatten the declarations.** Rejected: a block's doc documents its
  members, and docs/api would have lost it for the docless ones.

## References

- platform PR #54 — the dialect, `kit gen`'s codes and the import
- `internal/kernel/errs/CLAUDE.md` — the registry audits
