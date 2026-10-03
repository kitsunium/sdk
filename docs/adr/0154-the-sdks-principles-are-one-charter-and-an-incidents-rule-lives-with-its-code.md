# ADR 0154 — the SDK's principles are one charter, and an incident's rule lives with the code it hit

- **Status**: Accepted
- **Date**: 2026-10-03
- **Deciders**: SDK maintainers
- **Supersedes**: [ADR 0071](0071-a-registry-refuses-what-it-cannot-store.md), [ADR 0072](0072-health-bounds-every-wait-it-owns.md), [ADR 0073](0073-session-waits-are-abandonable.md), [ADR 0082](0082-the-lock-path-is-a-file-never-a-link-to-one.md), [ADR 0083](0083-a-path-is-a-chain-and-a-held-lock-can-lose-its-file.md), [ADR 0084](0084-the-windows-lock-directory-has-an-answer-and-it-is-not-a-mode.md), [ADR 0086](0086-creating-an-entry-is-not-replacing-one-and-windows-says-so-in-two-bits.md), [ADR 0087](0087-the-root-a-caller-named-is-a-spelling-it-did-not-choose.md), [ADR 0088](0088-a-suite-nothing-runs-is-not-a-test-suite.md), [ADR 0093](0093-a-sweep-takes-the-zombie-never-the-status.md), [ADR 0095](0095-windows-runs-every-test-and-gates.md), [ADR 0132](0132-a-floor-a-caller-names-is-the-floor-applied.md), [ADR 0137](0137-a-lane-that-loops-over-modules-reads-the-census.md), [ADR 0138](0138-a-doc-link-resolves-or-it-is-not-written.md) — each as a decision record; the text of every one is kept as the record of its incident
- **Related**: [ADR 0155](0155-every-layer-groups-its-packages-by-family-and-a-path-may-move-while-v0.md), [ADR 0156](0156-the-public-module-links-the-standard-library-and-nothing-else.md), [ADR 0157](0157-one-module-per-vendor-released-with-the-sdk.md), [ADR 0158](0158-distribution-mechanisms-are-the-frameworks-not-the-sdks.md), [ADR 0159](0159-the-kernel-holds-what-the-domains-rewrote-and-is-published-by-nature.md), [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md), [ADR 0161](0161-an-untyped-error-fails-the-build.md) — the reorganisation this charter opens

## Context

Before this record the SDK had 128 ADRs. Read end to end, about thirty of them
state a principle that every later domain followed — a zero value is never
inert, a published port grows by siblings, a message never names the value —
and each principle is written once, inside the ADR of the domain that first
needed it. A contributor
who wants the rules reads the domains; a contributor who reads one domain finds
one rule and not the twenty-seven others. The root `CLAUDE.md` tried to be the
digest and became a 36 000-character Purpose paragraph that repeated the ADR
index, which is the drift its own rule 11 forbids.

At the same time about seventeen ADRs are not, or not only, decisions about
the SDK's shape. They are incident reports: a defect found, reproduced on the
shipped code, fixed, and the rule that would have prevented it. Their rule is sound and
durable; their *place* is wrong. The rule belongs to the package it protects —
`lock`'s refusal of a planted link, `health`'s bound on a supervisor that stops
reading — and a reader of that package's `CLAUDE.md` does not look in
`docs/adr/` for it. An ADR is immutable, so the index keeps growing with
records whose live content is somewhere else, or nowhere.

A tree audit of 2026-10-02 (twenty read-only reports over `main`) extracted
sixteen cross-cutting principles from ADRs 0001–0065 and twelve from
0066–0151, and named the incident write-ups. This record consolidates the
first and relocates the second, without rewriting history: no ADR's body
changes, only its `Status` line and the indexes.

## Decision

### 1. The principles

Each principle names the ADRs it was decided in. When one of them and this list
disagree, the ADR's own text is the decision and this list is corrected.

**Structure**

1. **Layers point one way.** The kernel builds the core, the core builds the
   services, a complete service is exported through `pkg/v1`, and the framework
   sits above `pkg/v1` and imports nothing else of the SDK but `kernel/errs`.
   Nothing imports upward, and the direction is asserted on the build graph by
   `scripts/check-layer-deps.sh`, never left to visibility. (ADR 0001, 0004,
   0068, 0147)
2. **The kernel is stdlib-only AND generic.** Both halves, and nothing else: a
   consumer count was never the bar, and domain vocabulary is a disqualifier
   (`level` left the kernel for it). (ADR 0002, 0010, 0025, 0049, 0083, 0159)
3. **A domain is a core and a service.** The core holds what a second
   implementation would have to accept — ports, values and error codes; the
   service holds the mechanisms, every parser and encoder of a wire format
   included; `pkg/v1` publishes the complete service by alias, pointing at the
   layer that owns each type. (ADR 0074, 0160)
4. **The tree is grouped by family**, the same families in every layer, at
   whatever depth the families need; every codec lives in one tree. (ADR 0155)
5. **A dependency is quarantined where it enters, measured.** The public module
   links the standard library and nothing else; a vendor integration is a
   module of its own; a domain is split along the package a dependency really
   arrives in, not along the domain's name. (ADR 0012, 0034, 0066, 0079, 0156,
   0157)

**Errors**

6. **Every error is typed.** `errs.Define` / `errs.Wrap`, a dotted-quad
   `MM.LL.PP.SS` code, a wire-safe `Public` and a log-only `Private`; origin
   wins on wrap; an untyped error fails the build. (ADR 0002, 0005, 0006, 0019,
   0020, 0161)
7. **A code's value never changes.** A range belongs to one package, is
   allocated in the change that introduces its codes, and keeps its value when
   its declaration moves — `LL` records the layer that allocated it.
   (ADR 0005, 0035, 0147, 0160)
8. **A message never names the value.** No `Public`, no `Private`, no field
   quotes the input, a secret, a document or an argument; a refusal names the
   rule, the bound and the place. (ADR 0046, 0061, 0063, 0064, 0096, 0102,
   0141)

**The published surface**

9. **A published port grows by a sibling interface, never by widening.**
   (ADR 0039, 0067, 0092, 0097, 0104, 0139)
10. **A published shape or import path changes only while v0, and says so.**
    (ADR 0040, 0155)
11. **A port named in public is implementable in public.** (ADR 0074, 0090)
12. **No registry unless its key is safe and its set closed**, and a registry
    refuses at the call that publishes it what it cannot store. A key a
    stranger writes (`alg`) or an open type set (`Store[V]`) gets no registry.
    (ADR 0026, 0042, 0049, 0057, 0071)
13. **A foreign ecosystem is an adapter at the public edge**, never a
    dependency of a domain. (ADR 0032, 0062)
14. **stdout belongs to the consumer**: no SDK default writes to it. (ADR 0030)

**Behaviour**

15. **A zero value is clamped or refused, never inert.** (ADR 0031)
16. **Refuse by name, and say what is not guaranteed** — where a reader looks,
    as loudly as what is. (ADR 0041, 0046, 0052, 0054, 0056, 0063, 0156)
17. **A value that cannot know says so.** It degrades visibly with a reason; it
    never answers empty, and a probe that did not answer is not an answer.
    (ADR 0076, 0087, 0100)
18. **Text a stranger wrote can make a check stricter, never looser**, and is
    decoded one way or not at all. (ADR 0051, 0063, 0069, 0102)
19. **A secret is never rendered** — by any verb, any encoder, any log line.
    (ADR 0096, 0097, 0101)
20. **Verify before trusting, fail closed, and make every anchor rotatable.**
    (ADR 0077, 0091, 0150)
21. **A durable write joins the caller's transaction.** (ADR 0139, 0143, 0151)
22. **A mechanism decides; the caller performs.** (ADR 0080, 0121, 0149)
23. **Every wait is bounded by a budget nobody can walk away with, and observes
    its caller's context.** (ADR 0050, 0060, 0072, 0073)
24. **A standard is implemented from its document, with no vendor SDK.**
    (ADR 0044, 0047, 0048, 0051, 0063, 0064, 0156)

**Platforms**

25. **Every platform answers on its own terms.** A platform the code does not
    serve gets `UNSUPPORTED_PLATFORM`, chosen by file suffix; a platform it does
    serve is measured on its own kernel rather than assumed to be Linux.
    (ADR 0018, 0081, 0084, 0095, 0144)

**Enforcement and evidence**

26. **A rule nothing checks is a suggestion.** Enforcement runs at build time,
    over source, in a gate CI names: a suite nothing runs is not a suite, a lane
    that loops over modules reads the census, a doc link resolves or is not
    written. (ADR 0033, 0035, 0068, 0088, 0094, 0137, 0138, 0161)
27. **Measure before deciding, and keep the measurement.** A defect is
    reproduced on the shipped code before it is fixed, a guard is tested with
    its accepting rows beside its refusing ones, and a number in a document was
    measured, never deduced. (ADR 0034, 0049, 0053, 0058, 0081, 0082, 0083)
28. **Documentation is generated or checked, never trusted.** READMEs come from
    doc comments, indexes are checked against the files on disk, and a doc
    travels with the code in the same change. (ADR 0008, 0138; root `CLAUDE.md`
    rule 11)

### 2. An incident's rule lives in the package it hit

An ADR whose subject is an incident — a defect found, reproduced and fixed —
is superseded by this charter once its rule is written into the `CLAUDE.md` of
the package or script it protects, under a section titled **Rules from ADR
NNNN**: the rule as a short imperative, and the incident's lesson in one line.
The ADR file stays, unedited but for its `Status`, as the record of what
happened and why the rule exists; a reader of the package no longer needs it to
follow the rule.

The fourteen superseded here, and where each rule now lives:

| ADR | Incident | Rule now in |
|---|---|---|
| 0071 | a typed nil and a non-comparable plug-in passed every registrar's `== nil` | `internal/kernel/plugin/CLAUDE.md` |
| 0072 | a deaf supervisor stopped every later probe; a run every caller left was never cancelled | `internal/service/health/CLAUDE.md` |
| 0073 | a blocking `flock` and a mutex no cancellation could reach | `internal/service/session/CLAUDE.md` |
| 0082 | a planted link redirected the lock and its fencing ledger | `internal/service/lock/CLAUDE.md` |
| 0083 | a link at a parent moved the lock directory; a held lock lost its file | `internal/service/lock/CLAUDE.md`, `internal/kernel/pathchain/CLAUDE.md` |
| 0084 | the Windows lock directory was accepted whatever its DACL said | `internal/service/lock/CLAUDE.md` |
| 0086 | one Windows mask for two questions, and the files' inherited list unread | `internal/service/lock/CLAUDE.md` |
| 0087 | a root reached through a link answered false to every query | `internal/service/vcs/git/CLAUDE.md` |
| 0088 | the release scripts' suites ran in no lane | `scripts/CLAUDE.md` |
| 0093 | the reaper took the status a `Process` was waiting for | `internal/service/proc/childwait/CLAUDE.md` |
| 0095 | nineteen packages failed on Windows behind `continue-on-error` | `.github/workflows/CLAUDE.md` |
| 0132 | a writer gate read the floor `Info` as "inherit" | `internal/service/writer/levelgate/CLAUDE.md` |
| 0137 | four module lists had drifted, and two modules ran in no 32-bit lane | `scripts/CLAUDE.md` |
| 0138 | 152 doc links rendered as literal brackets | `pkg/v1/CLAUDE.md` |

The same treatment applies to an incident ADR written later: it may be written
as an ADR while the fix is argued, and is folded and superseded once its rule
has a home.

### 3. How the charter changes

A principle is added, reworded or withdrawn by an ADR that amends this one and
names the principle by number; the list here is then updated in the same change
and the number of a withdrawn principle is not reused. A domain ADR that
departs from a principle says so in its own text, citing the number.

## Consequences / Semantics

- **Implemented by this change**, which is documentation only: this record, the
  fourteen `Status` lines, their **Rules from ADR NNNN** sections (fifteen —
  ADR 0083's rule lives in two documents), and the three ADR indexes (`docs/adr/CLAUDE.md`, `docs/CLAUDE.md`, the root
  `CLAUDE.md` Reference list), which keep every superseded ADR listed.
- **Implemented by the reorganisation series** (ADR 0155 to 0161): the
  principles those records introduce or tighten — 3, 4, 5, 6, 7 and 26 in the
  parts that are new — land with their own code changes, and each of those
  records says which parts are done.
- The root `CLAUDE.md` Purpose paragraph becomes a short statement and a table
  of domains, one line each; the digest of what each ADR decided stays in
  `docs/adr/CLAUDE.md`, where `scripts/pre-commit/check-domain-docs.sh` checks
  it against the files on disk.
- A superseded ADR is still cited where its reasoning is the reason — the
  measurement behind a rule is in the incident record and nowhere else.

## Breaking changes

None. No code, no published shape, no behaviour.

## Alternatives considered

- **Rewrite and merge the ADRs** into one per domain. Rejected: an ADR is
  immutable after merge (`docs/adr/CLAUDE.md`), and a merged text loses the
  order in which things were learned — the reason most of these rules exist.
- **Keep the incident ADRs as they are, and only add the charter.** Rejected:
  the rule stays where the reader of the package does not look, and the next
  incident in the same package is argued without it.
- **Put the principles in the root `CLAUDE.md`.** Rejected: that file is loaded
  into every session, so its size is paid by every session, and a digest there
  is what had already grown to 36 000 characters; a charter is a decision with a
  history, which is what `docs/adr/` is for.

## Deferred

- The incident halves of ADR 0085, 0089 and 0135 (the release scripts) are not
  folded here: each of those records also decides how a release is sized, which
  is not an incident. They are candidates for the next pass.
- No check verifies that a superseded ADR's rule still appears in the
  `CLAUDE.md` the table names.

## References

- `docs/adr/CLAUDE.md` §Conventions — immutability, and the incident rule this
  record adds.
- The root `CLAUDE.md` — the twelve SDK-wide rules, the operational form of
  principles 1, 2, 5, 6, 7, 26 and 28.
