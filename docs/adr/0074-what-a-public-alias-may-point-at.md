# ADR 0074 — a public alias points at the layer that OWNS the type, and a single engine's configuration is owned by that engine

- **Status**: Accepted
- **Date**: 2026-09-12
- **Deciders**: SDK maintainers
- **Related**: [ADR 0001](0001-sdk-go-multimodule-layout.md) (the four layers), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (what publishing a type commits to), [ADR 0068](0068-layer-firewall-is-a-checked-graph.md) (the direction is checked on the graph, not inferred)

## Context

`pkg/v1` is aliases. `pkg/CLAUDE.md` said what a name may alias, in two bullets:
values and ports alias `internal/core` — naming "configuration structs" among
them — while engine HANDLES and their `Option` closures alias
`internal/service`. Any other alias onto a service type was "a layering defect
worth fixing".

Counted, the tree holds **233 aliases onto core, 69 onto service and 3 onto
kernel**, and the 69 are not 69 defects. Roughly forty of them are a single
engine's construction parameters — `sql.Config`, `session.FileConfig`,
`queue.FileConfig`, `lock.FileConfig`, `health.Config`, `lifecycle.RunConfig`,
`token.IssuerConfig`, `mail.SMTPConfig`, `metrics.OTLPHTTPConfig`, the five
`resilience` policy configs — and the rule as written condemned all of them
while listing `resilience.RetryConfig` as a legitimate service alias in the very
next bullet. The rule contradicted itself because "configuration struct" was the
wrong axis.

Hoisting those into core is not a neutral move. `sql.Config` names a `*sql.DB`
and a dialect; `session.FileConfig` names a directory, a file mode policy and a
poll interval; `mail.SMTPConfig` names a TLS mode `net/smtp` understands. Moving
them up would put one implementation's vocabulary into the layer whose whole job
is to describe what EVERY implementation must satisfy — and a second backend
would then either inherit fields that mean nothing to it or need a second config
beside the "shared" one.

## Decision

The axis is **ownership**, not value-versus-handle:

- **A type the PORT speaks lives in core.** Anything that crosses an interface
  declared in `internal/core` — what a method takes, returns, or a consumer
  implements — is owned by the contract, because a second implementation must
  produce and accept exactly it. `corenet.IdentityParams` and
  `corenet.IdentityFileParams` remain the example of a pair that must not
  separate.
- **A type meaningful to exactly ONE engine lives with that engine**, and
  `pkg/v1` aliases it there: its construction parameters, its handle, its
  `Option` closures, and any value only it can produce. This is the common case
  and it is not a defect. The test is a question with an answer in the code:
  *would a second implementation of this domain's port have to accept this
  type?* If it would not — because the type names a driver, a directory, a wire
  library or an engine's own state — it belongs to the engine.
- **The refusal that remains.** A type the port speaks that is nevertheless
  DECLARED in a service package is still a defect, and the reason is unchanged:
  it puts one concept on both sides of the boundary and lets the halves drift.
  The rule did not become permissive; its subject changed.

`pkg/CLAUDE.md` §Conventions is rewritten to this, and `resilience.RetryConfig`
stops being an exception listed under a bullet that excluded it.

## Consequences

- The 69 service aliases are audited against the ownership question rather than
  against a shape, and the count stops being read as a debt.
- A reviewer asking "why is this not in core?" has a question to answer instead
  of a rule to cite.
- Nothing moves. This ADR records a rule the code already follows and the
  documentation did not describe.

## Breaking changes

None. No type moved, no alias changed.

## Why not

- **Move the ~40 configs into core and keep the old rule.** It puts `*sql.DB`, a
  filesystem path and an SMTP TLS mode into the contract layer, which is the
  coupling the layer split exists to prevent — and ADR 0068 would then be
  checking a direction that no longer means anything.
- **Mirror each config with a core twin the engine converts from.** Two structs
  per engine that must agree field for field, with the conversion as the place
  they drift; the SDK would be paying for a boundary nothing crosses.
- **Forbid aliasing service entirely and export concrete types from `pkg/v1`.**
  Then `pkg/v1` stops being aliases and starts being a second implementation of
  every constructor, which is the layer this module exists to avoid.
- **Make it mechanical.** The question — would a second implementation accept
  this? — is not decidable from the source: a config naming only stdlib types
  can still be one engine's. The graph direction stays checked (ADR 0068); this
  rule stays argued.

## References

- `pkg/CLAUDE.md` §Conventions (rewritten by this ADR).
- The inventory behind the numbers: aliases resolved per file through their
  import paths across `pkg/v1/**`, 2026-09-12.
