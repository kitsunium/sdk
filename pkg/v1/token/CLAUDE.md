# pkg/v1/token/

## Purpose

The public facade for the SDK's security-token domain (ADR 0042): JWT over JWS
Compact Serialization (RFC 7519) and PASETO v4.public. Type aliases onto
`internal/core/token` + thin delegates onto `internal/service/token` and — for
loading a published key — `internal/service/crypto/jwk`, plus two generic
helpers (`PrivateClaim` / `SetPrivateClaim`) that keep `encoding/json` out of
the core value type. Stdlib-only, so a consumer gains no dependency.

Consumer-facing prose lives in the package doc comment (`token.go`), which
`gomarkdoc` renders into `README.md` (rule 10). This file is the maintainer's
half.

## Contents

| File | Surface |
|---|---|
| `token.go` | package doc + the type aliases (`Algorithm`, `Claims`, `Issuer`, `Verifier`, the four `*Config`) + `NewClaims` / `PrivateClaim` / `SetPrivateClaim` |
| `constants.go` | the four `Algorithm*` values and the seven `Claim*` names |
| `constructors.go` | `Key` / `JWK` / `JWKSet` aliases + the ten issuer/verifier `New*` delegates + the three JWK loaders `ParseJWK` / `ParseJWKSet` / `NewJWKSet` |
| `sentinels.go` | the 29 verdict vars, re-exported from core, service and `service/crypto/jwk` (the six `JWK*` parse refusals) |
| `codes.go` | the 29 `Code*` constants, re-exported for `errs.HasCode` |

## Why the codes are re-exported

`codes.go` declares `const CodeExpired errs.Code = coretoken.CodeExpired` and
twenty-eight siblings. These are **re-exports, not declarations**: the ranges
`0.2.13.*`, `0.3.44.*` and `0.3.42.*` stay owned by `internal/core/token`,
`internal/service/token` and `internal/service/crypto/jwk`, and the ADR 0035
ownership audit skips a cross-package selector for exactly this case ("pkg/v1
aliasing an internal sentinel does not make it an owner"). For the same reason
`scripts/gen-error-codes.sh` never lists them: it matches literal `= 0x…`
declarations only, so `docs/error-codes.yaml` keeps one entry per code, under
its owner.

They exist because matching on a code is a stronger contract than matching on a
reason string: a code is a number in `docs/error-codes.yaml`, a reason is a
spelling. A consumer routing an HTTP handler writes
`errs.HasCode(err, token.CodeExpired)` and gets a compile error if the constant
is ever removed.

## Why the JWK loaders are here

`JWK` and `JWKSet` alias `service/crypto/jwk.KeyValue` / `Set`, whose fields are
all unexported, so the alias alone publishes a type no consumer can BUILD: every
function that constructs one lives under `internal/`, which Go forbids a
consumer to import. `NewVerifierFromJWK` and `NewSetVerifier` shipped in exactly
that state — taking an argument nothing public could produce — and
`wiring_external_test.go` hid it by importing `internal/service/crypto/jwk` to
build its keys. ADR 0042 excludes FETCHING a key document, not PARSING one: its
D2 has the relying party fetch the document and hand it over.

`ParseJWK` / `ParseJWKSet` / `NewJWKSet` are one-line delegates onto
`jwk.Parse` / `jwk.ParseSet` / `jwk.NewSet`, so the one validating entry point
stays the only one. The six parse refusals (`0.3.42.1`–`.6`) are re-exported as
`JWK*` / `CodeJWK*` — prefixed, because `Malformed` and `KeyNotFound` already
name TOKEN verdicts in this package. The other five `0.3.42.*` sentinels
(`NoPublicForm` .7, `NoPrivateMaterial` .8, `TypeMismatch` .9, `KeyNotFound`
.10, `AmbiguousKid` .11) are never returned by loading — only by methods on the
aliased types — and are not re-exported.

The whole `token_test` package now imports stdlib and `pkg/v1` only (check:
`cd pkg && GOWORK=off go list -f '{{.XTestImports}}' ./v1/token/`), so a JWK
entry point that only the SDK's own packages could feed stops compiling rather
than passing. `TestAMalformedJWKIsRefusedWithAMatchableCode` carries one row per
re-exported code, and each row also publishes its bad member second in a set
whose first member is valid — which is what catches a set decoder that skips a
refused member instead of refusing the document.

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
- **Do not import `internal/` from a `_external_test.go` file here.** The
  external test package is the consumer's seat: once it reaches past the facade
  it can no longer notice a facade no consumer can use, which is how the JWK
  defect above shipped.
- Do not add an `UnmarshalJSON`, or any second decoding route, for `JWK` or
  `JWKSet`. `ParseJWK` / `ParseJWKSet` delegate to `jwk.Parse`, the single place
  its checks live — see `internal/service/crypto/jwk/CLAUDE.md` §Why there is no
  `UnmarshalJSON`.

## Verification

```sh
bazel test --config=race //pkg/v1/token:token_test
# Fallback
cd pkg && GOWORK=off go test -race -cover ./v1/token/...
```
