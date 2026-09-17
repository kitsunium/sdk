# pkg/v1/entitlement/

## Purpose

Thin **public facade** over `internal/service/entitlement` (ADR 0079): decide
whether this machine is entitled to run this build, against a vendor-signed
roster, and keep deciding when the network is gone. Consumers import this
package; the service and `core/entitlement` stay internal.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Identity` | type alias | `= coreent.Identity` — the three-method port, FROZEN |
| `BoundProver` | type alias | `= coreent.BoundProver` — the opt-in sibling that binds a proof to the authorised fingerprint (ADR 0092) |
| `Roster` / `Subject` / `CIEntitlement` | type alias | what a signed roster carries |
| `Grant` | type alias | what a successful verification hands back — read `Deadline()`, not the `NotAfter` field, to act on it |
| `Origin` | type alias | one place a roster is published |
| `Product` | type alias | `= svcent.ProductValue` — one engine's parameters (ADR 0074) |
| `Service` | type alias | `= svcent.Service` — the engine handle (ADR 0074) |
| `New` | func | delegates verbatim to `svcent.NewService` — one anchor, which IS the one-element list |
| `NewWithAnchors` | func | several vendor keys, ordered, so a signing key can be rotated in band (ADR 0091) |
| `RosterLifetime` | const | 24 h — the window a signed roster stays usable |
| `Code*` | const | the fifteen codes, for `errs.HasCode` |
| `Err*` | var | the fourteen sentinels, for `errors.Is` |
| `Bundle` | type alias | the one-document roster form, for a caller that serves or caches one |
| `Getter` | type alias | the roster HTTP surface, substitutable in a consumer's own suite |
| `UpdateRequiredError` | type alias | the version-floor refusal, carrying both versions |
| `NewWithGetter` | func | a verifier whose fetches go through your client |
| `RequiresUpdate` / `UpdateRefusal` | func | the version floor, without a Service |

**Both spellings of "why" are exported, and they must agree.** The codes follow
`pkg/v1/authz` and `pkg/v1/selfupdate`; the sentinels follow `pkg/v1/vfs` and
`pkg/v1/cache`. A consumer of THIS domain must distinguish "cannot decide" from
"decided no", and it will reach for `errors.Is` or for `errs.HasCode` depending
on where it came from — a facade offering only one would force the other half of
its callers onto message text.

The first cut of this package exported fifteen `Code*` and **not one** sentinel,
which made it unusable by the consumer it was written for.
`TestTheFacadeCarriesBothWaysOfAskingWhy` pairs every sentinel with its code so a
mismatched re-export fails rather than drifts, and the suite lives in an
`_test` package with no access to the internal layers — a test that imported
them would have passed against the incomplete facade.

## Bring your own identity

`New` takes an `Identity`, not a directory:

```go
type Identity interface {
	Discover() (subject string, err error)
	Fingerprint(subject string) (fingerprint string, err error)
	ProvePossession(subject string) error
}
```

Three methods, none of which says "ssh". A consumer with a TPM, a cloud KMS, a
hardware token or its own key format implements them and inherits nothing else.

**And one optional fourth, on a sibling.** The three cannot express "prove
possession of the key the ROSTER authorised": the engine asks `Fingerprint` what
you present, compares that answer itself, and then asks `ProvePossession`, so the
authorised value never reaches your code and anything that replaced the material
in between is signed for. Implement `BoundProver` —
`ProvePossessionFor(subject, authorised string) error` — and the verifier prefers
it; do not, and nothing changes. A sibling rather than a fourth parameter because
`Identity` is published through an alias and Go satisfies interfaces structurally,
so widening would break every implementation at compile time — and would not even
guarantee the binding, since a signature can be satisfied and its argument
ignored. ADR 0039, ADR 0040 §4, ADR 0092.

The SDK's own ssh implementation is `third-party/entitlement.NewSSHIdentity(dir)`
and lives in the ROOT module, because `golang.org/x/crypto/ssh` reaches
`golang.org/x/sys` — which is why importing THIS package adds zero modules to a
consumer's graph (ADR 0079 §3, measured).

## The anchor is a list, and what that costs

`New` takes one vendor key. `NewWithAnchors` takes an ORDERED list, and a roster
is authentic when it verifies against **any** entry — which is the whole of what
a key rotation needs: publish under B, installations carrying `{A, B}` accept it,
installations carrying only `{A}` keep reading A until they are updated, and
neither side has to move first. `Service.WithAnchors` sets the list on a verifier
already built; it is reachable through this package because `Service` is an alias
(ADR 0074), not because the facade re-declares it.

A single anchor had **no path in band**: a roster signed by a new key was refused
as a forgery *before* the mandatory-update floor was read, and the release
carrying the new anchor was refused by the anchor it replaced. Blocked both ways.

The cost is stated rather than mitigated. An anchor on the list is a key whose
**compromise is accepted while it is listed**, and nothing here changes that —
a client cannot be told "stop trusting A" by a document signed with A. What bounds
it is that the list is ordered, capped, and a **build** decision: no roster field,
no environment variable and no cache file can extend it. Past the cap the tail is
dropped and a line is logged. An **empty** list refuses every document with
`ErrRosterUnsigned`; it is not a way to switch the signature check off.

## The distinction that matters most

`CodeRosterUnreachable` is **not** a refusal. It says "no origin answered and
nothing was cached", and a caller that treats it as "not entitled" turns a
network outage into a revocation. Every other code in the range is a decision;
that one is the absence of one.

| Class | Codes | What a caller should do |
|---|---|---|
| cannot decide | `CodeRosterUnreachable` | keep serving, tell the operator |
| refused | `CodeRevoked`, `CodeLicenceExpired`, `CodeKeyMismatch`, `CodeNoPossession` | stop |
| not enrolled | `CodeNoLicence`, `CodeAmbiguousLicence` | enrol, or disambiguate |
| tamper | `CodeRosterUnsigned`, `CodeClockRegressed` | **do not retry** |

## The deadline is yours to honour

`Verify` computes a `Grant`, hands it back, and keeps no reference to it. **No
goroutine, no timer and no callback in this SDK revokes anything when a grant's
deadline passes**, and a process holding an expired grant is not interrupted.
Whatever gates work on a grant has to ask — so a grant expiring a second after a
check keeps authorising until the consumer looks again, and how long that is is
the consumer's tick.

What this package owes is a deadline that can be acted on **without polling**:

| Read | When |
|---|---|
| `Grant.Deadline()` | to SCHEDULE — the effective instant, zero-`NotAfter` fallback already resolved |
| `Grant.Expired(now)` | to JUDGE — the same rule asked the other way round |
| `Grant.NotAfter` | to DISPLAY what the verification recorded, which is the zero instant when it recorded none |

Reading the field to schedule on is the mistake the method exists to prevent:
`serve.go` seeds a grant from a bare timestamp at start-up, so `NotAfter` is
genuinely zero there and the deadline the SDK applies is
`VerifiedAt + RosterLifetime`. `Expired` used to be the only place that
fallback existed.

## What is NOT answered

A frozen clock. Every source of time an offline process can read belongs to the
party being checked; the ratchet raises an attacker's cost and does not close the
hole. Said here rather than buried in an ADR, because a consumer's threat model
depends on it.

## README is generated

`README.md` comes from `gomarkdoc` (ADR 0008). Regenerate with
`make docs-readme` — which runs `cd pkg/v1 && go generate ./...`, not `gomarkdoc`
from the repository root. Do **not** hand-edit it.

## Verification

```sh
bazel test --config=race //internal/service/entitlement:entitlement_test
```
