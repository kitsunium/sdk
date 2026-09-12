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
| `entitlement.go` | the package doc and the `Identity` port |
| `roster.go` | `RosterValue`, `SubjectValue`, `CIEntitlementValue`, `RosterLifetime` |
| `grant.go` | `GrantValue` — what a successful verification hands back |
| `origin.go` | `OriginValue` — one place a roster is published |
| `codes.go` / `errors.go` | the range (fifteen codes) and its fourteen sentinels |

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
- **`RosterUnreachable` is not a refusal.** It says "cannot decide", and a
  caller that treats it as "decided no" turns a network outage into a
  revocation. It is the single most important distinction in the range, which
  is why it has its own code rather than sharing one with `Revoked`.
- **`SubjectFor` reports absence as revocation.** A subject that was never
  approved and one that was removed are indistinguishable from a roster, and the
  safe reading of both is the same. Its doc comment says so where a caller reads
  it, rather than leaving the merge implicit.
- **`GrantValue` carries the deadline, not just the instant.** A daemon aging
  against `VerifiedAt` alone kept serving past the roster window that authorised
  it. The deadline is the roster's, so the grant cannot outlive its evidence.

## Do NOT

- Add a key format, a file path, a URL scheme or an environment-variable name
  here. Those belong to one product's distribution and live in the service's
  `ProductValue`.
- Add a "verification disabled" value however it is spelled. The zero
  `RosterValue` entitles nobody, which is ADR 0031's requirement for this
  domain.

## Verification

```sh
bazel test --config=race //internal/service/entitlement:entitlement_test
```
