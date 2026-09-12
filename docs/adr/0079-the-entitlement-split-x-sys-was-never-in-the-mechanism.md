# ADR 0079 — the entitlement split: x/sys was never in the mechanism, only in the identity

- **Status**: Accepted
- **Date**: 2026-09-12
- **Deciders**: SDK maintainers
- **Supersedes**: the Decision of [ADR 0078](0078-entitlement-is-quarantined-because-ssh-brings-x-sys.md) (its Context and its three inner decisions stand)
- **Related**: [ADR 0034](0034-hcl-quarantine-rationale-corrected.md) (measure what a dependency introduces), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (what a public alias may point at), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a refusal is not a degradation), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) (the code range), [ADR 0035](0035-pp-range-ownership-enforcement.md) (range ownership)

## Context

ADR 0078 put the whole entitlement domain under `third-party/` because
`golang.org/x/crypto/ssh` reaches `golang.org/x/sys`, which is banned SDK-wide.
It recorded the split as the better long-term shape and deliberately did not do
it, on the grounds that doing both at once would make the versement impossible
to diff against its source:

> **Split it: the stdlib-pure 92% into `pkg/v1`, the SSH identity into
> `third-party/`.** […] It is the better long-term shape and it is deliberately
> not what this change does: it doubles the work and would have split a
> versement across two module boundaries on its way in.

That reason has expired. The versement landed, it is on `main`, and its diff
against the source implementation is a matter of record. What remains is a
mechanism nobody can reach without also pulling the AWS SDK, ClickHouse, Mongo,
protobuf, redis, mysql and OpenTelemetry into their graph — for a check whose
whole purpose is to run in a single distributed binary.

The quarantine was also drawn around the wrong thing. Measured, not assumed:

| Reaches `x/crypto/ssh` | Lines (production) |
|---|---|
| no — roster, signature, cache, ratchet, CI seat, roughtime, version floor | 4 146 |
| yes — key parsing, fingerprint, possession, enrolment | 689 |

`x/sys` is in the IDENTITY, not in the mechanism. Quarantining the mechanism
with it was a boundary drawn along the package it happened to arrive in.

## Decision

**Split the domain along the four-layer contract, and quarantine only the ssh
implementation.**

```
internal/core/entitlement/     561 lines   the Identity port + the roster values + 15 codes
internal/service/entitlement/ 3436 lines   the engine: roster, cache, ratchet, CI seat, roughtime
pkg/v1/entitlement/            149 lines   the facade — aliases, New, 15 codes
third-party/entitlement/       689 lines   the ssh Identity implementation + enrolment
```

### 1. The seam is a three-method port, and none of them says "ssh"

```go
type Identity interface {
	Discover() (subject string, err error)
	Fingerprint(subject string) (fingerprint string, err error)
	ProvePossession(subject string) error
}
```

That is the entire surface the verification engine needs from the machine: which
subject it claims to be, what fingerprint the roster should hold for it, and that
the claim can be proven. Nothing about where key material lives, what format it
is in, or who may read it.

`ProvePossession` returns only an error because a nil error IS the proof.
Anything else returned here would be a second thing the caller had to verify.

### 2. `third-party/entitlement` becomes one implementation of that port

`SSHIdentity` wraps a key directory and implements the three methods over the
material the user already has, in OpenSSH format. Enrolment —
`GenerateKeyPair`, `IssueURL`, `NewSubjectID` — stays with it, because minting
an identity is the same policy as reading one.

Its compile-time proof lives in `sshidentity_compliance.go`, so a method added
to the port fails in the SDK's own build rather than at a consumer's.

### 3. The public API gains the mechanism and no dependency

Measured on the `pkg` module, `GOWORK=off`, before and after:

```
modules in pkg's graph @ HEAD:  8
modules in pkg's graph after:   8
difference: none
```

Zero. The service's only non-stdlib import is `golang.org/x/mod/semver`, for the
version floor a roster can mandate, and `x/mod` was already in that graph.
`internal/service/go.mod` and `pkg/go.mod` still contain no `x/sys` — checked,
not assumed.

## Consequences

- A consumer that manages its own key custody — a TPM, a cloud KMS, a hardware
  token, a key file of its own format — implements three methods and gets the
  roster, the offline cache, the anti-rollback ratchet, the CI seat and the
  version floor from `pkg/v1/entitlement`, inheriting none of the root module.
- A consumer that wants the ssh identity requires the root module and its graph,
  exactly as before. Nothing regressed for them; the alternative simply exists
  now.
- The domain claims code range `0.2.35.*` (`0x00_02_23_*`) in `internal/core`,
  vacating `0.3.65.*`. Hand-registered in `codeRangeOwners` and in
  `//:audit_sources`, per ADR 0035.
- `ProductValue` stays in `internal/service` and is aliased from `pkg/v1` as
  `Product`, per ADR 0074: it is one engine's construction parameters — origins,
  cache directory, OIDC audience, enrolment URL — not something a second
  implementation of the port would have to accept.
- Three copies of the product-name fallback ("entitlement" when a product names
  none) collapse into one exported `ProductValue.Label`. The source
  implementation carried two; the split would have made it three.

## Breaking changes

For anything importing `third-party/entitlement` by pseudo-version: `Service`,
`NewService`, `ParseRoster`, `ParseBundle`, `VerifyCI`, `ProductValue`, the
roster values and the fifteen `Code*`/`Err*` symbols moved. Their new homes are
`pkg/v1/entitlement` (public) or the internal layers behind it.

The root module is never tagged — `cut-tags.sh` chains
`internal/{kernel,core,service}` and `pkg` only — so `third-party/entitlement`
was reachable exclusively by pseudo-version, and only between ADR 0078 and this
record — both dated 2026-09-12, with no release cut in between.

`NewService` now takes a `coreent.Identity` where it took an ssh directory
string. A caller keeping the previous behaviour passes
`entitlement.NewSSHIdentity(dir)`.

## Alternatives considered

- **Leave it under `third-party/` and wait for a second consumer.** ADR 0078's
  own Deferred section says that is the trigger. The trigger arrived in the same
  week — ktn-linter needs the mechanism and already owns its ssh identity — and
  waiting would have meant migrating the linter onto a package it would then
  have had to be migrated off.
- **Move the ssh identity into `internal/service` behind a build tag.** A build
  tag cannot keep a module out of `go.mod`: the import is still there for
  `go mod tidy` to resolve, so `x/sys` would enter `pkg`'s graph for every
  consumer whether they compiled it or not.
- **Re-implement OpenSSH key parsing against the stdlib.** Unchanged from ADR
  0078: it is the one piece of this domain where a parsing bug is a security
  bug, and `x/crypto/ssh` is the reference implementation.
- **Put `Identity` in `internal/kernel`.** The kernel is stdlib-only AND
  generic. A port named after entitlement subjects and fingerprints is neither.

## Deferred

Carried forward unchanged from ADR 0078, none of them addressed here:

- **The frozen clock.** Every source of time an offline process can read belongs
  to the party being checked. The ratchet raises the cost; it does not close the
  hole.
- **`RoughtimeServers` ships EMPTY.** The mechanism is implemented and tested;
  naming a server list would make every consumer depend on a host the SDK does
  not operate.
- **`fmt.Errorf` in the service.** The engine came across with the source
  implementation's error construction, which predates SDK rule 2. The fifteen
  sentinels are `errs.Define`-typed and the wrapping uses `%w`, so `errors.Is`
  and `errs.HasCode` both work through it; the conversion is a separate change
  that touches every refusal path and deserves its own diff.
- **The cache's rename on Windows, and `rememberRoster`'s lost update.** Both
  are recorded on the versement's review threads and neither is reachable from
  this CI matrix.

## References

- [ADR 0078](0078-entitlement-is-quarantined-because-ssh-brings-x-sys.md) — the quarantine this splits, and the Context that still holds
- [ADR 0034](0034-hcl-quarantine-rationale-corrected.md) — the probe method used to measure both boundaries
- [ADR 0074](0074-what-a-public-alias-may-point-at.md) — why `Product` and `Service` alias the service layer
- ed25519 — RFC 8032, https://www.rfc-editor.org/rfc/rfc8032
