# ADR 0092 — a possession proof that cannot name the key it is about

**Status**: Accepted; implemented in `internal/core/entitlement/entitlement.go`, `internal/service/entitlement/service.go`, `third-party/entitlement/sshidentity.go`.
**Date**: 2026-09-16
**Deciders**: kitsunium maintainers
**Applies**: ADR 0039 (a published port is extended by a sibling, never by widening) and ADR 0040 §4 (the v0 licence does not cover interfaces)
**Related**: ADR 0079 (the entitlement split that made `Identity` a port)

## Context

`matchSubject` performs the last gate of a verification in three steps:

```go
got, printErr := s.identity.Fingerprint(subject)   // what do we present?
if got != want.Fingerprint { … }                   // the ENGINE compares
if proveErr := s.identity.ProvePossession(subject) // prove it
```

The fingerprint the roster **authorised** never traverses the port. So an
implementation of `Identity` cannot bind its proof to it: it is asked "what do
you present", then separately "prove you hold something", and it signs with
whatever the second call finds. Everything that can write the key material gets
to act in the window between the two — a rotation landing mid-verification, a
mounted volume remounted, a `<uuid>.pub` replaced on purpose — and the
implementation has no way to notice, because it was never told which key the
decision was about.

The ceiling is not theoretical and it has been measured by a consumer. The
`ktn-linter` consumer pushed the detection as far as three methods allow — it
memoises the key object across the two calls and refuses a proof when nothing is
memoised — and recorded the residual limitation in its own
`pkg/license/CLAUDE.md`. That is *change detection*. It is not *binding*: it can
tell that something moved, never what it was supposed to be.

The shape the consumer proposed is `ProvePossession(subject, authorised string)`:
one parameter, no new method, refusal taxonomy unchanged.

**That shape is refused, and the capability is not.**

## Decision

1. **`Identity` stays FROZEN at three methods, byte-identical.** It is reachable
   through `pkg/v1/entitlement.Identity`, a type alias, so ADR 0039's rule binds:
   *any port reachable through a `pkg/v1` alias is frozen in the same way*. ADR
   0040 §4 removes the escape — "is it an interface? Extend with a sibling. **The
   version does not matter**; structural satisfaction breaks callers at any
   version." The v0 licence covers concrete shapes and explicitly not this.

2. **The capability ships as a sibling**, `coreent.BoundProver`, with one method:

   ```go
   ProvePossessionFor(subject, authorised string) error
   ```

3. **`matchSubject` prefers it by type assertion** (`(*Service).prove`), and falls
   back to `ProvePossession` when it is absent. The absence is how a consumer
   discovers which world it is in — the same mechanism ADR 0052 uses for
   `Deadliner` and ADR 0049 for `EntryFetcher`.

4. **The refusal taxonomy does not grow.** A bound proof whose material is not
   the authorised material refuses with `coreent.ErrKeyMismatch` — "a local key
   that does not match the published fingerprint", which is precisely what
   happened. No new code, no sixteenth sentinel.

5. **The SDK's own ssh implementation implements it**, and the binding is the
   whole of what it adds: `ProvePossessionFor` re-reads the key directory,
   compares `Fingerprint(pub)` against `authorised` by byte equality in the
   roster's own spelling, and signs over **that** read. The comparison and the
   signature now happen against one read of the directory, which is the property
   two separate calls cannot have. The refusal names the subject in its FIELDS
   and neither the subject nor the authorised value in its wire-safe sentence —
   this refusal is reachable by anyone who can write the key directory, which is
   the worst possible place to echo back what the roster published.

6. **The freeze is guarded executably**, as ADR 0039 §2 requires rather than by
   comment: `TestAThreeMethodDoubleStillSatisfiesIdentity` declares a bare
   three-method type and assigns it to `Identity`, and
   `TestAThreeMethodDoubleIsNotABoundProver` asserts the sibling is a separate
   contract. Folding the method into the port fails the first to COMPILE.

## What this does NOT close

**An `Identity` that does not implement `BoundProver` keeps the window.** The
engine cannot bind a proof for an implementation that has not offered to be
bound, and nothing here can change that. Stated in the port's own doc comment, in
`prove`'s, and here — not as a caveat in one place and a guarantee in another.

**Binding is not freshness.** `ProvePossessionFor` proves that the material
present at the moment of the call is the authorised material and that its private
half answers a challenge. It says nothing about the instant after it returns, and
a proof is a statement about a moment by construction.

**The engine's own comparison stays.** `matchSubject` still compares
`Fingerprint`'s answer against the roster, and deliberately: the cheap comparison
runs before the signing round trip, it is what produces `ErrKeyMismatch` for a
three-method identity, and removing it would move a refusal every implementation
currently produces into one that only some do.

## Consequences

- One new interface in `internal/core/entitlement`, aliased in `pkg/v1/entitlement` beside `Identity`: the
  facade publishes `Identity` because consumers implement it; `BoundProver` is
  reached through the same alias file only when a consumer wants to implement it,
  and so is exported from core and aliased in `pkg/v1/entitlement` beside it.
- `(*Service).prove` is the only dispatch point. `matchSubject` is one line
  different.
- `SSHIdentity` gains `ProvePossessionFor` and an unexported `answer` shared with
  `ProvePossession`, so the challenge cannot drift between the two — the bound
  method adds a comparison *before* the proof and changes nothing about the proof.
- `sshidentity_compliance.go` asserts both contracts at compile time. The engine
  reaches the sibling by type assertion, which cannot fail a build, so an
  implementation that silently stopped satisfying it would fall back to the
  unbound proof and nothing would say so. That line is what makes it a compile
  error instead.
- No new error code, no `codeRangeOwners` entry, no dependency, no behaviour
  change for any identity that does not implement the sibling.

## Breaking changes

None. `BoundProver` is a new interface beside `Identity`, not a change to it —
`port_internal_test.go` freezes `Identity` at three methods, so folding the
method in would fail to COMPILE rather than break a consumer quietly. An
existing implementation that does not satisfy `BoundProver` keeps working
through the unbound path, with the window this ADR describes.

## Why widening was refused, and it is not only the ADR

The ADRs settle it, but the decisive argument is that widening does not buy what
it appears to buy.

**It would not guarantee binding.** An implementation can satisfy
`ProvePossession(subject, authorised string) error` and ignore `authorised`
entirely — it compiles, it returns nil, nothing anywhere notices. Binding is the
implementation's choice under either shape. What widening *does* guarantee is
that every existing implementation stops compiling until somebody edits it, which
is a cost with no matching benefit.

**And the breakage is larger than adding a method.** Adding a method leaves the
three an implementer already wrote intact. Changing a signature invalidates one
of them, in a language where the compiler's message names a method the
implementer believed was correct. In this repository alone the blast radius is
`SSHIdentity`, four test doubles across three packages, and every double a
consumer's suite declares; outside it, `ktn-linter`'s own implementation and
anyone else's, none of which this repository can see.

**The sibling is strictly less work for the consumer that asked.** Adding one
method is additive; changing a signature is a migration. The consumer keeps its
memoisation as the fallback for the day it is talking to an older SDK, and drops
it when it no longer is.


## Migration

**Nothing breaks.** `Identity` is unchanged, so every existing implementation —
in this repository and out of it — compiles and behaves exactly as before.

For an implementation that wants the binding:

1. add `ProvePossessionFor(subject, authorised string) error`;
2. inside it, read the key material **once**, compare its published fingerprint
   against `authorised`, and refuse with `ErrKeyMismatch` if they differ;
3. sign over that same read;
4. keep `ProvePossession` — the port is frozen and a caller holding a plain
   `coreent.Identity` must keep working;
5. assert both contracts with `var _ coreent.BoundProver = …`, because the engine
   reaches the sibling by assertion and a silent loss of it is a silent
   downgrade.

## Deferred

- **A verifier that ASKS for the bound path.** `matchSubject` still calls
  `Fingerprint` then `ProvePossession`, because the three-method port it holds
  cannot pass the authorised value. Making the verifier prefer `BoundProver`
  when the identity satisfies it is the change that turns this interface into
  a closed window rather than an available one, and it is a behaviour change
  to the verification path — its own review.
- **The compliance assertion in consumers.** `var _ entitlement.BoundProver`
  cannot be written by a consumer until this interface is in a PUBLISHED
  release. Interface satisfaction is structural, so an implementation pairs
  with it the moment it ships; the assertion follows that release rather than
  preceding it.
- **Collapsing the two calls entirely.** A `Present(subject) (fingerprint
  string, prove func() error, err error)` shape would remove the window
  without a second interface. Refused rather than deferred, for the reason in
  the section above: a closure in a port is harder to implement correctly than
  a string parameter. Recorded here so the option is not re-proposed as new.

## References

- `internal/core/entitlement/entitlement.go` — `Identity` (frozen) and `BoundProver`
- `internal/core/entitlement/port_internal_test.go` — the two freeze guards
- `internal/service/entitlement/service.go` — `(*Service).prove`
- `internal/service/entitlement/possession_internal_test.go` — the value crossing the port, the bound refusal, and the fallback
- `third-party/entitlement/sshidentity.go` — the binding over real key files
- `third-party/entitlement/sshidentity_external_test.go` — the swap between the two calls, with the unbound proof as the control
