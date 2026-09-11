# ADR 0035 — PP-range ownership is enforced by an audit the audited code cannot edit

- **Status**: Accepted
- **Date**: 2026-09-09
- **Deciders**: SDK maintainers
- **Related**: [ADR 0005](0005-sdk-error-codes-dotted-quad.md) §Registry (the invariant), [ADR 0006](0006-sdk-error-code-registry-extension.md) (allocation extension), [ADR 0020](0020-errs-audit-dual-reason-derivation.md) (`Code*` naming the audit relies on)
- **Amends**: nothing. It supplies the enforcement ADR 0005 §Registry assumed.

## Context

ADR 0005 states that each package owns a `PP` slot in the `MM.LL.PP.SS`
keyspace. The root `CLAUDE.md` rule 3 described that allocation table as
"mirrored by the AST audit in `internal/kernel/errs/registry_external_test.go`".

It was not. `TestAuditCodeUniqueness` keys on the **fully resolved numeric Code
value** — deliberately, and correctly, because it was written to close a
different blind spot (V1/V93/V100: two identifiers folding to one value). But
two packages declaring codes in the same `MM.LL.PP` with different `SS` bytes
produce **no duplicate value at all**. That is the literal definition of range
squatting, and it passed green.

Uniqueness of codes and ownership of ranges are two invariants. Only the first
was checked.

Two further gaps made the situation worse than "one missing check":

1. **Define-keyed collection is blind to declarations.** The audit collects
   `errs.Define` call sites. `internal/core/codec` declares the whole `0.2.2.*`
   block and never calls `Define` — it formats the code into its registry
   errors. A range that is allocated, documented and used was invisible.
2. **`docs/error-codes.yaml` cannot be the authority.** It is generated *from*
   the constants. An audit checking constants against a table derived from those
   same constants would record any squatter as the legitimate owner and stay
   green — the check would validate whatever it found.

An inventory taken before writing the check found **54 ranges, each declared by
exactly one package, with no collision**. This is therefore a verification gap,
not a data defect: nothing was wrong, and nothing prevented it from going wrong.

## Decision

1. **Ownership is keyed on Code CONSTANT DECLARATIONS**, not on `Define` call
   sites. A package owns a range by declaring constants in it, however it later
   emits them.

2. **The owner table is hand-maintained and independent of the audited tree.**
   `codeRangeOwners` in `internal/kernel/errs/registry_ownership_external_test.go`
   maps `MM.LL.PP` → owning package directory. It is written by hand, never
   generated. Being able to disagree with the code is the entire point.

3. **Two checks, because either alone leaves a hole.**
   - `TestAuditPrefixExclusivity` — no range is declared by two packages. Catches
     squatting on an *occupied* range.
   - `TestAuditPrefixOwnership` — every declared range is present in the table
     and matches. Catches a package moving into a range that is *reserved but
     unused*, which exclusivity cannot see.

4. **Allocation is just-in-time.** A new range is added to `codeRangeOwners` in
   the same change that introduces its codes. No speculative reservation of
   ranges for work that has not started. **A published code is never
   renumbered** to make the table fit; the table is corrected instead.

5. **Scope, stated rather than assumed.**
   - Cross-package selector values (`core.CodeFoo`) are **re-exports, not
     definitions**, and confer no ownership.
   - `iota`-based groups are skipped: the only one is kernel/errs' Layer-0
     meta-code block, and Layer 0 is already **enforced at runtime** by
     `validateDefineArgs`, which panics on a `Define` with `Layer==0` outside its
     whitelist. That is a stronger mechanism than this audit.
   - A `Code`-typed constant not named `Code*` is type machinery, not an
     allocation — kernel/errs' CIDR masks (`MaskByMajor` … `MaskExact`) are
     `Code`-typed by design. `Code*` naming is the convention ADR 0020 already
     depends on.
   - Platform variants of one package share a directory, hence one owner, with
     no special case.

6. **The checks are proven to fail.** `TestAuditPrefixChecksDetectViolations`
   exercises four fixtures against the *same* detection functions the real
   audits call: same range in two packages with different `SS`; a range owned by
   another package; a range absent from the table; and many serials of the
   rightful owner, which must be accepted. A green audit that has never been
   shown to fail verifies nothing — the lesson rule 12 records.

## Consequences

- Adding a domain now costs one line in `codeRangeOwners`, in the same commit as
  its codes. Forgetting it fails `TestAuditPrefixOwnership` with the range, the
  package and the file position.
- Root `CLAUDE.md` rule 3's claim that the audit mirrors the allocation table
  becomes true, and now names both audit files.
- The table duplicates information that also lives in ADR 0005/0006 prose. That
  duplication is deliberate: the ADR is the decision record, the table is the
  executable check, and they are meant to be able to disagree loudly.

## Why not

- **Derive the table from `docs/error-codes.yaml`.** Rejected — §Context.2: it
  is generated from the constants, so it would legitimise a squatter.
- **Extend `TestAuditCodeUniqueness` instead of adding files.** Rejected: it
  answers "is this code unique", a question with its own regression history.
  Overloading it with a second invariant would blur what a failure means.
- **Exclusivity only, no owner table.** Rejected: it cannot protect a reserved
  range, which is exactly what a reservation is for.
- **A `go vet` tool or linter rule.** Rejected: `tools/*` cannot take
  `golang.org/x/tools` without breaking its Bazel build (ADR 0033 records the
  same constraint). The audit lives where the kernel is stdlib-only.

## References

- ADR 0005 §Registry, ADR 0006, ADR 0020 · rule 3 and rule 12 in root `CLAUDE.md`
- `internal/kernel/errs/registry_ownership_external_test.go`
- `internal/kernel/errs/registry_external_test.go` (`TestAuditCodeUniqueness`,
  the value-keyed audit this one complements)
