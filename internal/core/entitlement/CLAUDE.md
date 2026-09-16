# internal/core/entitlement/

## Purpose

The contract for deciding whether this machine is entitled to run this build
(ADR 0079): the one port the decision needs from its environment, the values a
vendor-signed roster carries, and the fourteen sentinels a refusal can name.
Interfaces and immutable values only.

Code range `0.2.35.*` (`0x00_02_23_*`), owned solely by this package.

## Contents

| File | Role |
|---|---|
| `entitlement.go` | the package doc, the `Identity` port and its `BoundProver` sibling |
| `roster.go` | `RosterValue`, `SubjectValue`, `CIEntitlementValue`, `RosterLifetime` |
| `grant.go` | `GrantValue` — what a successful verification hands back |
| `origin.go` | `OriginValue` — one place a roster is published |
| `codes.go` / `errors.go` | the range (fifteen codes) and its fourteen sentinels |
| `grant_internal_test.go` | the three dates a grant is bounded by |
| `roster_internal_test.go` | the public/private split of both roster lookups |
| `port_internal_test.go` | the freeze, guarded by a compile rather than by a comment |

## Why-this-shape

- **`Identity` is three methods, and none of them says "ssh".** `Discover`,
  `Fingerprint`, `ProvePossession`. The verification engine needs to know which
  subject this machine claims to be, what fingerprint the roster should hold for
  it, and that the claim can be proven — nothing about how the material is
  stored. The ssh implementation lives under `third-party/` because
  `golang.org/x/crypto/ssh` reaches `golang.org/x/sys`, which is banned SDK-wide
  (ADR 0034). A consumer with its own key custody implements the three methods
  and inherits none of that graph.
- **`ProvePossession` returns only an error.** A nil error IS the proof.
  Anything else returned here would be a second thing the caller had to verify,
  and the first thing an attacker would try to forge.

- **The port is FROZEN at three methods, and `BoundProver` is how it grew
  anyway.** `matchSubject` asked `Fingerprint` what the machine presents,
  compared the answer ITSELF, then asked for a proof — so the AUTHORISED
  fingerprint never crossed the port and no implementation could bind to it.
  Material replaced between the two calls is signed for. The fix is ADR 0039's
  sibling and not a fourth parameter: `Identity` is reachable through
  `pkg/v1/entitlement.Identity`, so the freeze binds, and ADR 0040 §4 states the
  version does not matter for an interface. Past the ADRs, widening would not
  even buy the binding — an implementation can accept the parameter and ignore
  it — while guaranteeing that every implementer stops compiling.
  `TestAThreeMethodDoubleStillSatisfiesIdentity` fails to COMPILE if the method
  is folded in; its twin asserts the sibling is a separate contract, so the
  fallback path stays reachable. ADR 0092.

- **The sibling's ABSENCE is documented, not papered over.** An `Identity` that
  does not implement `BoundProver` keeps the window between the two calls open,
  and nothing here can close it for an implementation that has not offered to
  have it closed. Said in the port's doc comment, in `(*Service).prove`'s, and in
  ADR 0092 — never guaranteed in one place and caveated in another.
- **`RosterUnreachable` is not a refusal.** It says "cannot decide", and a
  caller that treats it as "decided no" turns a network outage into a
  revocation. It is the single most important distinction in the range, which
  is why it has its own code rather than sharing one with `Revoked`.
- **`SubjectFor` reports absence as revocation.** A subject that was never
  approved and one that was removed are indistinguishable from a roster, and the
  safe reading of both is the same. Its doc comment says so where a caller reads
  it, rather than leaving the merge implicit. A roster that LISTS a subject with
  an empty fingerprint joins them under the same sentinel and is separated only
  by the `condition` field: it is a broken publisher, not a withdrawal.
- **Both roster lookups are total on a nil receiver.** `Roster` is aliased into
  `pkg/v1/entitlement`, so a consumer can hold a nil one; `CIEntitlementFor` has
  refused it since it was written and `SubjectFor` used to dereference it. Both
  now refuse, fail-closed, with `condition=no roster to check against`. Not
  `RosterUnreachable`, which means "cannot decide, retry" — retrying a nil
  pointer never terminates.
- **`Deadline()` exists because the FIELD cannot answer the question a
  scheduler asks.** `NotAfter`'s zero value means "not recorded" — the shape a
  grant seeded from a bare timestamp carries — and the fallback to
  `VerifiedAt+RosterLifetime` lived inside `Expired` and nowhere else. So a
  consumer reading the field to schedule its next check read the zero instant and
  could not derive the deadline the SDK would actually apply; polling was its only
  option, which is how a grant expiring a second after a check keeps authorising
  until the next tick. `Expired` is now DEFINED in terms of `Deadline`, because
  two copies of one rule is how a field and a method come to disagree about when
  a grant died, and `TestGrantValueExpiredAndDeadlineCannotDisagree` probes one
  nanosecond either side rather than an hour. Honouring the deadline is the
  CONSUMER's job and nothing here enforces it — no goroutine, no timer, no
  callback — which is said in `Expired`'s own comment, on the field, and in the
  facade, rather than guaranteed in one place and caveated in another.

- **`GrantValue` carries the deadline, not just the instant.** A daemon aging
  against `VerifiedAt` alone kept serving past the roster window that authorised
  it. The deadline is the tightest bound in play, so the grant cannot outlive its
  evidence.

- **`GrantDeadline`'s bounds are VARIADIC because the rule is about documents,
  not about two of them.** It took the roster's window and the subject's term —
  exactly the two a DEVICE grant rests on — and a CI seat rests on a third, the
  Actions token that proved the run is real. So `ciseat.go` bounded a seat by the
  roster and lived for up to a day on a thirty-minute proof, contradicting the
  invariant `NotAfter`'s own comment states one file away. Variadic rather than a
  fourth parameter so the device call sites read unchanged; a caller with nothing
  extra to name passes nothing extra. Zero still means "not recorded" and is
  skipped, never minimised over, or every grant would be born dead.

- **`ErrRosterStale` covers two conditions, and the second is not a new
  situation.** A window that has CLOSED is the absolute bound. A roster
  SUPERSEDED by a newer signed decision the client already accepted is the
  relative one — its own window may be wide open and it is still the replay of a
  statement the vendor has replaced, which is the other half of the replay bound
  this sentinel has always advertised. Only the `condition` field separates them,
  because the resolving action does not differ: publish, or fetch, a current
  roster. A sixteenth code was considered and refused: a caller mapping sentinels
  to exit codes and advice lives downstream of this module, and a new one falls
  through to its default — the defect `authenticateRoster`'s unsentinelled decode
  failure already paid for once.
- **A refusal names no account, no uuid and no deadline.** `CIEntitlementFor`'s
  four refusals render one byte-identical sentence, and `SubjectFor`'s renders
  another; which account, which uuid, which term closed and when travel as
  `errs` FIELDS. A refusal that quotes the identifier back confirms it to
  whoever presented it, and a roster can be read one probe at a time that way.
  The `condition` field is what keeps the four tellable apart for the operator
  who is entitled to the difference — losing the particular and leaking it are
  both defects, and `TestNoParticularReachesThePublicSentence` asserts both
  directions.

## Do NOT

- Add a key format, a file path, a URL scheme or an environment-variable name
  here. Those belong to one product's distribution and live in the service's
  `ProductValue`.
- Fold `BoundProver`'s method into `Identity`, or add any other method to it.
  The port is published through a `pkg/v1` alias and Go satisfies interfaces
  structurally, so it breaks every downstream implementation at compile time with
  no deprecation window (ADR 0039, ADR 0040 §4). Extend by a sibling; two named
  tests fail the build if this is tried.
- Add a "verification disabled" value however it is spelled. The zero
  `RosterValue` entitles nobody, which is ADR 0031's requirement for this
  domain.
- Call `fmt.Errorf` here, or interpolate a particular into a public sentence.
  Refusals are `errs.Wrap(Sentinel, errs.WrapParams{}, errs.String(…))` — the
  shape every other `internal/core/*` package uses — so origin-wins keeps the
  sentinel's Code, Reason, Public, Private and exit status and the call site
  adds only fields.

## Verification

```sh
bazel test --config=race //internal/service/entitlement:entitlement_test
```
