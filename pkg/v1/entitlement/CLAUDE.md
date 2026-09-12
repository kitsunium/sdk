# pkg/v1/entitlement/

## Purpose

Thin **public facade** over `internal/service/entitlement` (ADR 0079): decide
whether this machine is entitled to run this build, against a vendor-signed
roster, and keep deciding when the network is gone. Consumers import this
package; the service and `core/entitlement` stay internal.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Identity` | type alias | `= coreent.Identity` — the three-method port |
| `Roster` / `Subject` / `CIEntitlement` | type alias | what a signed roster carries |
| `Grant` | type alias | what a successful verification hands back |
| `Origin` | type alias | one place a roster is published |
| `Product` | type alias | `= svcent.ProductValue` — one engine's parameters (ADR 0074) |
| `Service` | type alias | `= svcent.Service` — the engine handle (ADR 0074) |
| `New` | func | delegates verbatim to `svcent.NewService` |
| `RosterLifetime` | const | 24 h — the window a signed roster stays usable |
| `Code*` | const | the fifteen codes, for `errs.HasCode` |

The codes are re-exported deliberately, following `pkg/v1/authz` and
`pkg/v1/selfupdate`: a consumer of THIS domain must distinguish "cannot decide"
from "decided no", and a facade that hid the codes would force it to match on
message text.

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

The SDK's own ssh implementation is `third-party/entitlement.NewSSHIdentity(dir)`
and lives in the ROOT module, because `golang.org/x/crypto/ssh` reaches
`golang.org/x/sys` — which is why importing THIS package adds zero modules to a
consumer's graph (ADR 0079 §3, measured).

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
