# internal/core/token/

## Purpose

Declares the **security-token port**: the one-method `Issuer` and `Verifier`
contracts, the immutable `ClaimsValue` every concrete format decodes into, the
closed `Algorithm` enum, and the typed verdicts a caller matches on. The 13th
core sibling, admitted by **ADR 0042**. Formats are concrete and live in
`internal/service/token`; this package owns only the contract + the sentinels.

Code range: `0.2.13.*` (ADR 0042).

## The decision this package exists to make

**There is no registry, and that is the security decision.**

Every other pluggable SDK domain resolves an implementation through a
process-wide registry keyed on a name. A token registry would be keyed on the
`alg` header — a field the *attacker* writes. Resolving the verifying algorithm
from that field IS algorithm confusion: the class of bug where an RSA or EC
public key (public by definition) is fed to HMAC-SHA-256 as a shared secret and
the forgery verifies.

So the algorithm is bound at construction, by a constructor that accepts only
the one key type that algorithm can use, and the header is only ever *compared*
against that binding. `proc`, `resilience` and `net` are the no-registry
precedents; this is the first one where the absence is a security property
rather than a shape preference.

`Algorithm` is a `uint8` enum rather than a string for the same reason: the
unsecured `none` algorithm of RFC 7519 §6 has **no representation in the type**,
so no call site can request it and no configuration can enable it.
`TestNoAlgorithmSpellsNone` scans the whole `uint8` range to keep it that way.

## Contents

| File | Surface |
|---|---|
| `token.go` | `Algorithm` (+ `String` / `Known`), `Issuer`, `Verifier` |
| `claims.go` | the seven `Claim*` name constants, `MaxPrivateClaims`, `IsRegisteredClaim`, the immutable `ClaimsValue` + accessors + the redacting `String`/`GoString` |
| `claims_with.go` | the copy-on-write `With*` setters, including `WithPrivateRaw` |
| `codes.go` | `Code*` constants — range 0.2.13.\* |
| `errors.go` | `Malformed` (.1), `AlgorithmNone` (.2), `AlgorithmMismatch` (.3), `SignatureInvalid` (.4), `Expired` (.5), `NotYetValid` (.6), `ExpiryRequired` (.7), `AudienceMismatch` (.8), `IssuerMismatch` (.9), `TooLarge` (.10), `TooDeep` (.11), `KeyUnsuitable` (.12), `PolicyMisconfigured` (.13), `IssueFailed` (.14), `ClaimNameInvalid` (.15), `LifetimeTooLong` (.16) |

## Conventions

- **The ports are one method each, and stay that way.** `Verifier.Verify` and
  `Issuer.Issue` are aliased into `pkg/v1/token`, so ADR 0039 applies with full
  force: Go interfaces are structural, and adding a method would break every
  downstream double at compile time with no deprecation window. A new
  capability lands as a **sibling interface**, never as a widening.
- **The Verifier contract has three security clauses**, stated on the type:
  the signature is checked before any claim is read; the token's own algorithm
  header is compared, never used to select; and a failure returns exactly one
  typed verdict with the ZERO claim set — there is no "invalid, but here are
  the claims anyway" path.
- **`ClaimsValue` renders its SHAPE, never a value.** `%v` / `%s` / `%#v` all
  produce `token.Claims{registered:3 aud:1 private:2}`. A claim set is the
  output of an authentication decision and routinely holds a subject id, an
  email or a tenant; without `String`/`GoString`, `%#v` walks the unexported
  fields reflectively and dumps every one. The counts are enough to tell "no
  audience was sent" from "the audience did not match", which is what a `%v` is
  usually reaching for.
- **Private claims are RAW JSON.** This is the core layer, and "what shape is a
  scope claim" is the caller's question. Storing the exact bytes the issuer
  signed also means re-encoding can never change what was authenticated.
  Decode one with `pkg/v1/token.PrivateClaim`.
- **`WithPrivateRaw` copies the MAP, not just the header.** A value receiver
  copies the map header and shares the buckets, so a setter writing in place
  would edit every existing copy of the claim set — including one a verifier
  had already judged. `TestWithPrivateRawCopiesTheMap` pins it.
- **A registered claim name may not be shadowed.** A second `"exp"` that the
  temporal validator never reads is a claim an application would trust and
  nothing would enforce.
- **The zero `Time` means ABSENT, never the epoch.** Whether an absent `exp` is
  acceptable is a policy question, and the answer lives in
  `service/token.VerifierConfig.AllowMissingExpiry` — whose zero value requires
  one (ADR 0030: the default must not be the dangerous one).
- Sentinels carry `EX_NOPERM` (77) for a refusal, `EX_DATAERR` (65) for an
  unreadable input, and `EX_CONFIG` (78) for a broken issuer/verifier. Every
  refusal also carries **HTTP 401**: RFC 6750 §3.1 spends `invalid_token` on
  exactly this set, and a 400 would tell a client to change its request when
  the answer is to obtain a new token.
- **`Public` strings name the CHECK, never a value.** They are read by third
  parties: not the audience, not the issuer, not a key id, not a claim. A
  rejection message that quotes the token's own contents back is a mirror an
  attacker can query.

## Do NOT

- Put format bodies here — they live in `service/token`.
- Add an `Algorithm` value for `none`, in any spelling.
- Add a registry, or any function that maps a header field to an
  implementation. See above; this is the point of the package.
- Widen `Issuer` or `Verifier`. Add a sibling interface (ADR 0039).
- Add `context` to the ports: nothing here does I/O.
- Make `ClaimsValue` printable. The redaction is a security property, and it
  only fires because the whole method set is on a value receiver.

## Verification

```sh
bazel test --config=race //internal/core/token:token_test
# Fallback
cd internal/core && GOWORK=off go test -race -cover ./token/...
```
