# ADR 0034 — the HCL quarantine is right, its stated mechanism is not: x/sys is introduced, never downgraded

- **Status**: Accepted
- **Date**: 2026-09-09
- **Deciders**: SDK maintainers
- **Related**: [ADR 0022](0022-sdk-codec-hcl.md) (HCL codec quarantine), [ADR 0012](0012-logger-writer-registry.md) (third-party quarantine for vendor-heavy integrations), [ADR 0016](0016-sdk-process-supervision-domain.md) §Dependency discipline (the `x/sys` ban), [ADR 0018](0018-sdk-cross-platform-portability.md) §133 (the ban restated), [ADR 0029](0029-sdk-net-domain.md) §"The `x/sys` collision"
- **Amends**: [ADR 0022](0022-sdk-codec-hcl.md) §Context.1 and §Why not (the *mechanism*, not the decision)

## Context

ADR 0022 quarantines the HCL codec under `third-party/codec/hcl` instead of
`internal/service/codec`. Its first stated reason is a **version downgrade**:

> Adding it to `internal/service/go.mod` **downgrades shared dependencies** —
> observed: `golang.org/x/sys` `v0.33`→`v0.20` […]

That sentence has since been quoted as a general placement rule — "check the
`x/sys` impact before placing a dependency" — in the root `CLAUDE.md`, in
`docs/CLAUDE.md`, in `docs/adr/CLAUDE.md`, and in
`third-party/codec/hcl/CLAUDE.md`.

**The mechanism it describes cannot happen.** Go resolves versions by minimal
version selection: the selected version of a module is the **maximum** of the
versions required across the graph. Adding a dependency that requires a *lower*
version cannot lower a higher requirement that is still present.

Measured, Go 1.27.1, fresh module, `GOWORK=off`:

| Step | Selected `golang.org/x/sys` |
|---|---|
| `require golang.org/x/sys v0.33.0` | `v0.33.0` |
| `+ github.com/hashicorp/hcl/v2 v2.23.0` (which itself requires `x/sys v0.5.0`) | `v0.33.0` — **unchanged** |

Reproducing the *actual* scenario — HCL added to a copy of
`internal/service/go.mod` — shows something different, and sharper:

| Step | `golang.org/x/sys` in `internal/service` |
|---|---|
| before | **absent from the graph entirely** |
| `+ github.com/hashicorp/hcl/v2 v2.23.0` | **added at `v0.20.0`**, pulled by `golang.org/x/tools v0.21.1` (HCL itself asks only for `v0.5.0`) |

The git history agrees: `internal/service/go.mod` did not require `x/sys` before
commit `ce5c0a1`, does not require it after, and that commit adds HCL to the
**root** module. The `v0.33` figure in ADR 0022 belongs to a different module
than the one the sentence is about.

So the number was wrong, the direction was wrong, and the module was wrong —
while the conclusion was right, for a reason the ADR never stated.

## Decision

1. **ADR 0022's decision stands unchanged.** HCL stays in
   `third-party/codec/hcl`. Nothing about the placement, the opt-in
   registration, the capabilities, or the `0.3.37.*` block is affected.

2. **The mechanism is restated.** Adding `hcl/v2` to `internal/service` does not
   downgrade `x/sys`; it **introduces** it, at `v0.20.0`, into a module where
   `golang.org/x/sys` is **banned SDK-wide** (ADR 0016 §Dependency discipline,
   restated in ADR 0018 and ADR 0029). `internal/service/proc` is written
   against raw stdlib `syscall` precisely because of that ban — see
   `internal/service/proc/exec/limittable_unix.go` ("`golang.org/x/sys` (banned
   here)") and `internal/service/proc/rlimit/rlimit_unix.go`.

   A ban violation is a stronger reason than a version regression, and it is the
   reason that actually applies.

3. **The downgrade narrative is retired as a placement rule.** No document may
   cite "hcl downgrades `x/sys`" to justify a placement. The two criteria that
   do apply, and are sufficient on their own:
   - does the dependency introduce a **banned** module (`x/sys`, and anything
     that transitively pulls it) into a module that forbids it?
   - does it impose a dependency graph on consumers who never use the
     integration? `third-party/` isolates imports and their cost; it does **not**
     create two versions of one module in a binary.

4. **The historical incident is recorded as not reproduced**, not as
   disproven-in-every-form. A downgrade *can* be produced by a command that
   explicitly asks for one, by dropping a higher requirement, or by a
   `go mod tidy` that removes a stale over-specification. None of those is "an
   HCL dependency drags `x/sys` backwards", and the original conditions
   (command run, active `go.work`, resulting diff) were not recorded. Anyone who
   recovers them should amend this ADR rather than restore the old sentence.

## Consequences

- `docs/adr/0022-sdk-codec-hcl.md` keeps its text (ADRs are append-only) and
  gains a Status pointer to this ADR, so a reader meeting the downgrade
  sentence is sent here.
- The four `CLAUDE.md` files that quoted the narrative are corrected to the
  ban-introduction mechanism, in this change (rule 11).
- Future third-party placement arguments cite the ban and the consumer-graph
  criterion. A placement decision that rests on a claimed version movement must
  show the measurement, in the ADR, as this one does.

## Why not

- **Edit ADR 0022 in place.** Rejected: `docs/adr/CLAUDE.md` makes ADRs
  append-only, and silently rewriting a merged rationale destroys the record of
  what was believed when the decision was made — which is most of an ADR's
  value.
- **Reverse the quarantine.** Rejected: the decision is correct. HCL in
  `internal/service` would introduce a banned module into `proc`'s own module.
- **Leave it alone as a harmless inaccuracy.** Rejected: it had already been
  promoted from an observation into a cited rule in four documents, and a rule
  derived from a mechanism that cannot occur will eventually refuse a
  dependency for a reason that does not exist.

## References

- ADR 0022 §Context.1, §Why not · ADR 0012 (quarantine policy) · ADR 0016
  §Dependency discipline · ADR 0018:133 · ADR 0029 §"The `x/sys` collision"
- [Go modules reference — minimal version selection](https://go.dev/ref/mod#minimal-version-selection)
- `internal/service/proc/exec/limittable_unix.go`,
  `internal/service/proc/rlimit/rlimit_unix.go` (the ban, in code)
