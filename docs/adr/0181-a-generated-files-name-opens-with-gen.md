# ADR 0181 — a generated file's name opens with gen_

- **Status**: Accepted on 2026-10-20 by the owner
- **Date**: 2026-10-20
- **Deciders**: the owner, on the audit of kit's generated files
  (kitsunium/studio#74); kit's side is kitsunium/studio's ADR 0014
- **Amends**:
  - ADR 0163, which names the pins (`api_gen*_test.go`) and the ports
    (`design_gen.go`);
  - ADR 0164 (`codes_gen.go`, the ranges' `codes_gen_test.go`);
  - ADR 0165 (`perf_gen_test.go`);
  - ADR 0166 (`facade_gen.go`);
  - ADR 0167, which lists the files a doc digest covers;
  - ADR 0168 (`decl_gen.go`, `stubs_gen.go`);
  - ADR 0180 §5, which kept the product's file names (`*_gen.go`) as they
    were.

## Context

kit wrote its files under a `_gen` suffix:

- the pins `api_gen_test.go` and `api_gen_<n>_test.go`;
- the ports `design_gen.go`;
- the codes `codes_gen.go`, and the ranges' table `codes_gen_test.go`;
- the budgets' tests `perf_gen_test.go`;
- the facade `facade_gen.go`;
- the declarations `decl_gen.go`, and the scaffolding `stubs_gen.go`.

The audit of kitsunium/studio#74 counted 580 such files in the SDK, and
found three ways the suffix fails a reader or a tool:

- **The glob matches hand-written files.** `*_gen*.go` also matches
  `registry_generic.go` and `facade_generics_external_test.go`. That is why
  `make regen` deletes by header, never by name.
- **The suffix does not sort.** A package's generated files are scattered
  among its hand-written ones.
- **The ktn-linter skips them in two ways at once.** It has
  `skip_generated: true` and the exclude `**/*_gen.go`. So a defect kit
  writes, such as an assertion without its comment (KTN-COMMENT-VAR),
  reached no linter.

The owner's decision: a generated file's name always **starts** with
`gen_`, never ends with a suffix. The prefix is impossible to confuse with
anything else, and one glob, `gen_*.go`, names them all.

## Decision

### 1. The names

| Role | Before | Now |
|---|---|---|
| pins (ADR 0163) | `api_gen_test.go`, `api_gen_<n>_test.go` | `gen_api_test.go`, `gen_api_<n>_test.go` |
| ports (ADR 0163) | `design_gen.go`, `design_gen_<n>.go` | `gen_ports.go`, `gen_ports_<n>.go` |
| codes and sentinels (ADR 0164) | `codes_gen.go` | `gen_codes.go` |
| the code ranges' table (ADR 0164, `codes.owners`) | `internal/kernel/errs/codes_gen_test.go` | `internal/kernel/errs/gen_ranges_test.go` |
| budgets' tests (ADR 0165) | `perf_gen_test.go` | `gen_perf_test.go` |
| facade (ADR 0166) | `facade_gen.go` | `gen_facade.go` |
| declarations (ADR 0168) | `decl_gen.go` | `gen_decls.go` |
| `kit gen -stubs` (ADR 0168) | `stubs_gen.go` | `gen_stubs.go` |

A product's generated files move the same way: `gen_design.go`,
`gen_design_test.go`, `gen_wire.go` and `gen_main.go`. This replaces
ADR 0180 §5's "file names stay as they are". No hand-written file of the
SDK starts with `gen_`, and none may. The header (ADR 0163 §7) is
unchanged, and it is still the header that decides what kit owns:
`make regen` and `make from-zero` delete by header.

### 2. What moved

- **The files.** kit `v0.1.0-rc.14` is pinned (`project.kit.version`).
  `kit gen -rename` wrote every generated file under its new name with the
  same body; `codes.owners` names `gen_ranges_test.go`. 580 files moved.
- **genindex.**
  - `pinGlob` is `gen_api*_test.go` and `designGenGlob` is `gen_ports*.go`;
  - docs/api was written again, since a record names its file;
  - the READMEs and `docs/error-codes.yaml` are unchanged in substance.
- **The guards and scripts.**
  - `check-core-symmetry.sh` reads the owners' table at its new name;
  - `regen.sh`, `from-zero.sh` and `kit-coverage.sh` name the new files;
  - `.gitattributes` keeps `gen_*.go` at LF.
- **The BUILD files** list the new names, sorted, as gazelle writes them.
- **`.ktn-linter.yaml`.** The generated files are linted
  (`skip_generated: false`), and the exclude `**/*_gen.go` is gone. The
  rules that do not fit what kit writes, or that judge what the design
  decides, leave `**/gen_*.go` out one by one, each with its reason (#74,
  §5):
  - the facade's re-exports: KTN-CONST-TYPED, KTN-CONST-IOTA and
    KTN-STRUCT-COLOCATE;
  - the facade's block order, the design's while the owner has not decided
    to sort it: KTN-CONST-ORDER, KTN-VAR-ORDER and KTN-VAR-GROUP;
  - a port's names and signatures, the design's: KTN-INTERFACE-ERNAME,
    KTN-INTERFACE-ANYUSE and KTN-INTERFACE-FILENAME.

  Every other rule holds the generated files, and a finding there is kit's
  to fix. ktn-linter, built from source, finds nothing in phases 1–7.

## Consequences

- A generated file is known at a glance, and a package's generated files
  sort together.
- The ktn-linter holds kit to what it can meet.
- Where an ADR of this repository names a generated file by its former
  name, read the name in §1's table. Decided ADRs are not rewritten.

## References

- kitsunium/studio#74: the audit, its §4 (naming schemes) and §5 (lint
  policy)
- kitsunium/studio's ADR 0014: kit's side, including the window in which
  the former names are still kit's and `kit gen -rename`
