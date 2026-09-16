# ADR 0091 — a single trust anchor is a key with no way out

**Status**: Accepted; implemented in `internal/service/entitlement/anchors.go`.
**Date**: 2026-09-16
**Deciders**: kitsunium maintainers
**Amends**: ADR 0079 §trust model (the anchor is a list, not a key)
**Related**: ADR 0031 (a zero value is never inert — the clamp half is used here), ADR 0078 (the quarantine the identity port answers)

## Context

`internal/service/entitlement` held one field for the whole trust model:

```go
// vendor is the only trust anchor, linked into the binary.
vendor []byte
```

Everything the domain refuses is downstream of that one key. `ParseBundle`
verifies against it, `bundleMark` reads the ratchet's evidence through it, and
`cachedRoster` re-reads the offline copy through it. One key, every path.

**An installation whose anchor has to change therefore has no path in band**, and
it is blocked from both directions at once:

- serve it rosters signed by a new key B and it refuses them with
  `ErrRosterUnsigned` — the *spoofing* sentinel, raised against a document the
  vendor genuinely signed. That refusal lands in `rosterFrom`, which `Verify`
  reaches **before** `RequiresUpdate`, so the mandatory-update floor that exists
  precisely to say "upgrade this binary" is never read;
- ship it a release signed by B and the running installation refuses the release
  against the anchor A it still holds.

The only remaining route is out of band: an operator fetching a binary by hand,
on every machine, with no signed statement they can check. For a domain whose
entire purpose is that revocation is real for cooperative installs, the key that
makes revocation possible was the one thing that could not be replaced.

This is the last structural gap in the entitlement audit (constat F32). It is not
hypothetical: it is the situation a compromised signing key produces, and the
scheme's answer to a compromised signing key was "there is none".

The requirement that settles the shape is not new either, and it is right:
*the product embeds only public keys, **several at a time**, to permit rotation.*

## Decision

1. **The anchor is an ORDERED LIST, and a roster is authentic when it verifies
   against ANY entry.** That is the whole of what rotation needs and it needs
   nothing more: publish under B, installations carrying `{A, B}` accept it,
   installations carrying only `{A}` keep reading A's rosters until they are
   updated. **Neither side has to be updated first**, which is the property a
   single anchor cannot have at any ordering.

2. **Several accepted keys are still ONE signer.** Authentication establishes
   that the vendor issued these bytes and nothing else, so no quorum is being
   taken and none is offered: a threshold would answer a question this scheme
   does not ask, since there is one signer whose *key* changes rather than
   several parties who must agree.

3. **The list is ORDERED, BOUNDED at `maxAnchors = 4`, and a BUILD decision.**
   Nothing at runtime can extend it — no roster field, no environment variable,
   no cache file — and a key leaves the list the way it entered, by shipping a
   build without it. Two is what a rotation needs, three is a rotation
   interrupted by a second one, four is that with headroom. Past the bound the
   **tail** is dropped and a line is logged: the order is the build's statement
   of which anchor is current, so the tail is the only end droppable without
   overriding it.

4. **The bound CLAMPS rather than refusing** — ADR 0031's clamp half. The list is
   a build decision, so an over-long one is a build mistake, and answering a
   build mistake by refusing every verification would take a product down over a
   misconfiguration that one log line names precisely. This is the same trade the
   domain already makes for a contended cache lock.

5. **An EMPTY list refuses every document**, with `coreent.ErrRosterUnsigned` and
   the condition `no trust anchor was linked into this build, so no signature can
   be valid`. It is not a way to disable the signature check. **No sixteenth
   code**: `internal/core/entitlement/CLAUDE.md` records why one was refused —
   a caller mapping sentinels to exit codes and advice lives downstream of this
   module and a new code falls through to its default — so a new *condition*
   under an existing sentinel is the shape, exactly as `ErrRosterStale` already
   carries two.

6. **The loop moves on from a document an anchor cannot AUTHENTICATE, never from
   one it authenticated and the rules then refused.** This is `currentRoster`'s
   rule for origins, applied to keys for the same reason.
   `ErrRosterUnsigned` is precisely the first case; an undecodable bundle, a
   duplicated member name and a window that is over-wide, unopened or closed are
   properties of the **document** and identical under every anchor. Continuing
   past one of those is a real defect and not merely noise: an expired roster
   tried against a second anchor comes back `ErrRosterUnsigned`, so an operator
   whose publisher had simply fallen behind would be told somebody is
   impersonating the vendor.

7. **The ratchet reads its mark through the same list.** A bundle cached under an
   anchor that is still accepted is still the vendor's signed statement about
   when it was issued. Reading the mark against one key would drop the whole
   anti-rollback distance at exactly the moment — a key rotation — when an
   installation is least able to re-establish it. Both sides of the ratchet's
   comparison go through `bundleMarkAnyAnchor`, as both went through
   `bundleMark` before: a mark installed under one anchor set and compared under
   another is a ratchet with a seam in it.

8. **The single-key form is the one-element list, and is not a second
   representation.** `NewService(identity, vendor, product)` delegates to
   `NewServiceWithAnchors(identity, [][]byte{vendor}, product)`, so no path in
   the package can disagree with the multi-anchor one about what one key means. A
   `nil` vendor stays a one-element list holding a malformed key — **not** an
   empty list — so it keeps drawing the refusal it always drew, naming
   `key_bytes` rather than an absent anchor.

9. **No public caller breaks.** `pkg/v1/entitlement.New` keeps its signature
   byte-for-byte; `NewWithAnchors` is added beside it, and `Service.WithAnchors`
   sets the list on a verifier already built. `Service` is a type alias (ADR
   0074), so the setter is reachable through the facade without the facade
   re-declaring it. This is a data shape and a function set, never an interface:
   ADR 0039/0040's sibling rule is not engaged.

## The tension, recorded rather than resolved

**Accepting several anchors is strictly more surface than accepting one.** An
anchor on the list is a key whose compromise is **accepted** for as long as it is
listed — including the key whose compromise prompted the rotation, until the build
that drops it has actually been installed. There is no mechanism in this ADR that
mitigates that, and inventing one would be dishonest: a client cannot be told
"stop trusting A" by a document signed with A, and a revocation list signed by B
is worth nothing to an installation that does not yet accept B.

What bounds the surface is therefore procedural, and stated as such:

| | Single anchor | Ordered list |
|---|---|---|
| rotation in band | impossible | possible |
| keys accepted at once | 1 | up to `maxAnchors` |
| compromised key stops being accepted | never (no rotation exists) | when a build without it is installed |
| runtime can add an anchor | — | no, and there is no code path that could |

The list being a build decision is what keeps that table honest. The bound is what
keeps dropping a key a decision somebody has to make, rather than a slot that is
always free.

## Consequences

- `Service.vendor []byte` becomes `Service.anchors [][]byte`. Unexported, so no
  consumer is affected; eleven in-package test literals were updated.
- Two new production entry points — `svcent.NewServiceWithAnchors` and
  `(*Service).WithAnchors` — and one facade function, `NewWithAnchors`. Nothing
  removed, nothing re-typed.
- `parseBundleAnyAnchor` and `bundleMarkAnyAnchor` are the only readers of the
  list. `ParseBundle`, `ParseRoster`, `authenticateRoster` and `bundleMark` keep
  their single-key signatures: one anchor verifying one document stays the
  primitive, which is what keeps the multi-anchor rule reviewable in one file.
- No new error code, no `codeRangeOwners` entry, no new dependency.
- A verifier mid-rotation costs one extra `ed25519.Verify` per unmatched anchor on
  the failing path only — the matching anchor short-circuits, and the *common*
  case (the current key first) is byte-for-byte the old work.

## Breaking changes

None for a consumer. `NewWithAnchors` and `WithAnchors` are additions; the
single-key constructors keep their signatures and resolve to a one-element
list, so a build that passes one key behaves exactly as it did.

Breaking for a PUBLISHER that stamps only one anchor and expects the release
channel to be a recovery path for it: it never was, and this change makes the
product refuse that configuration rather than appear to have two roots of
trust while holding one. That refusal is the point of the ADR, so it is stated
here rather than left to be discovered at the first rotation.

## Why not

- **A second `vendorNext` field beside `vendor`.** Rejected: two fields is a
  seam, and the ratchet's own history in this package is the argument — a mark
  installed under one rule and compared under another is what
  `bundleMarkAnyAnchor`'s doc comment exists to prevent. One list, read in one
  place.
- **Let the roster name the next anchor.** Rejected outright: a document signed by
  A that installs anchor B makes A's compromise permanent, because whoever holds A
  can install any B they like. The set of acceptable keys cannot travel in
  something one of those keys signs.
- **An unbounded list.** Rejected: the cost of the list is exactly its length, and
  a bound that is never reached is a bound that lets the list become where retired
  keys accumulate.
- **Refuse at construction past the bound.** Rejected on ADR 0031's own line: an
  over-long list is a build mistake, and refusing every verification over it
  converts a misconfiguration into an outage. Clamping the tail and logging says
  the same thing without taking the product down.
- **A sixteenth error code for "no anchor linked in".** Rejected for the reason
  already recorded when a sixteenth was last considered: the consumer that maps
  sentinels to advice is downstream, and a code it does not know falls through to
  its default. The `condition` field carries the distinction.
- **Require a quorum of anchors.** Rejected: there is one signer. A quorum would
  also make every rotation a flag day, which is the problem being solved.

## Deferred

- **Rotation without a release.** An anchor list is a BUILD decision, so
  adding a key still requires shipping a binary. A roster-carried list would
  let the document name the keys that authenticate it, which is circular; a
  separate signed anchor document would need its own anchor. Deferred because
  every shape considered moves the problem rather than solving it.
- **Reading the ACL / key usage.** Nothing checks that a declared anchor is an
  ed25519 public key of the right length before it reaches verification; a
  malformed entry is simply one that never verifies. Cheap to add and not
  added here, because it changes no outcome an attacker can reach.
- **A bound above 4.** `maxAnchors` is 4 because a rotation needs two and an
  overlapping rotation needs three. Raising it is a one-constant change if a
  deployment ever needs it; lowering it is not, so the slack is deliberate.

## References

- `internal/service/entitlement/anchors.go` — the list, the bound, and the two readers
- `internal/service/entitlement/anchors_internal_test.go` — rotation accepted, off-list refused, empty list refuses everything, and the mark read through the second anchor
- `pkg/v1/entitlement/entitlement_external_test.go` — `TestNewWithAnchorsAcceptsEitherAnchorDuringARotation`, through the public surface alone
- `internal/core/entitlement/CLAUDE.md` — why there is no sixteenth code
