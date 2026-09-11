# pkg/v1/token/

## Purpose

The public facade for the SDK's security-token domain (ADR 0042): JWT over JWS
Compact Serialization (RFC 7519) and PASETO v4.public. Type aliases onto
`internal/core/token` + thin delegates onto `internal/service/token`, plus two
generic helpers (`PrivateClaim` / `SetPrivateClaim`) that keep `encoding/json`
out of the core value type. Stdlib-only, so a consumer gains no dependency.

Consumer-facing prose lives in the package doc comment (`token.go`), which
`gomarkdoc` renders into `README.md` (rule 10). This file is the maintainer's
half.

## Contents

| File | Surface |
|---|---|
| `token.go` | package doc + the type aliases (`Algorithm`, `Claims`, `Issuer`, `Verifier`, the four `*Config`) + `NewClaims` / `PrivateClaim` / `SetPrivateClaim` |
| `constants.go` | the four `Algorithm*` values and the seven `Claim*` names |
| `constructors.go` | `Key` / `JWK` / `JWKSet` aliases + the ten `New*` delegates |
| `sentinels.go` | the 23 verdict vars, re-exported from core and service |
| `codes.go` | the 23 `Code*` constants, re-exported for `errs.HasCode` |

## Why the codes are re-exported

`codes.go` declares `const CodeExpired errs.Code = coretoken.CodeExpired` and
twenty-two siblings. These are **re-exports, not declarations**: the ranges
`0.2.13.*` and `0.3.44.*` stay owned by `internal/core/token` and
`internal/service/token`, and the ADR 0035 ownership audit skips a cross-package
selector for exactly this case ("pkg/v1 aliasing an internal sentinel does not
make it an owner").

They exist because matching on a code is a stronger contract than matching on a
reason string: a code is a number in `docs/error-codes.yaml`, a reason is a
spelling. A consumer routing an HTTP handler writes
`errs.HasCode(err, token.CodeExpired)` and gets a compile error if the constant
is ever removed.

## Why `NewClaims` and not `NewClaimsValue`

The core constructor is `NewClaimsValue`, because `KTN-STRUCT-ROLE` requires the
`Value` suffix on a core value type. That suffix is a layer convention, not a
consumer's concern, so the facade publishes the shorter `NewClaims`. Same
reason `Claims` aliases `ClaimsValue`.

## Do NOT

- **Do not add a `NewVerifier(algorithm, key)` convenience.** The whole design
  is that no such call exists: the constructor set is what makes algorithm
  confusion unwritable, and one "ergonomic" wrapper taking an algorithm name
  would hand it back. See ADR 0042 and `internal/service/token/CLAUDE.md`.
- Do not widen `Issuer` or `Verifier` — they are aliased ports, so ADR 0039
  applies: a sibling interface, never a widening.
- Do not add a constant, a field or a flag that admits `alg: none`.
- Do not hand-edit `README.md`. Edit the package comment in `token.go` and run
  `make docs-readme` (rule 10 / ADR 0008).
- Do not make `Claims` printable, or add an accessor that renders its contents.
  The shape-only rendering is a security property of the core type.

## Verification

```sh
bazel test --config=race //pkg/v1/token:token_test
# Fallback
cd pkg && GOWORK=off go test -race -cover ./v1/token/...
```
