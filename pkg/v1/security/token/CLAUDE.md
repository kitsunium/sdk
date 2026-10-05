# pkg/v1/security/token/

## Purpose

The public facade for the SDK's security-token domain (ADR 0042): JWT over JWS
Compact Serialization (RFC 7519) and PASETO v4.public. Type aliases onto
`internal/core/security/token` + thin delegates onto `internal/service/security/token` and — for
loading a published key — `internal/service/crypto/key/jwk`, plus two generic
helpers (`PrivateClaim` / `SetPrivateClaim`) that keep `encoding/json` out of
the core value type. Stdlib-only, so a consumer gains no dependency.

Consumer-facing prose lives in the package doc comment (`doc.go`, which kit
writes from `design/security/token.yaml`, ADR 0167); `README.md` is written by
`tools/genindex` from the committed `docs/api` (`make docs-readme`). This file
is the maintainer's half.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167): the fragments `codes.go`, `constants.go`, `constructors.go` and `sentinels.go` held, joined in file-name order when their declarations moved to `facade_gen.go` (ADR 0166), then the package doc proper |
| `token.go` | `PrivateClaim` / `SetPrivateClaim` |
| `facade_gen.go` | kit's, from `design/security/token.yaml`'s `facade:` (ADR 0166): the type aliases (`Algorithm`, `Claims`, `Issuer`, `Verifier`, the four `*Config`, `Key` / `JWK` / `JWKSet` / `KeyType` / `Curve`), `NewClaims`, the four `Algorithm*` values, the seven `Claim*` names and the JWK vocabulary (three `KeyType*`, four `Curve*`), the ten issuer/verifier `New*` forwarders and the three JWK loaders `ParseJWK` / `ParseJWKSet` / `NewJWKSet`, the 29 verdict vars re-exported from `core/security/token` (all 23 token verdicts, ADR 0160) and `core/crypto/key/jwk` (the six `JWK*` parse refusals), and the 29 `Code*` constants re-exported for `errs.HasCode` |

## Why the codes are re-exported

`facade_gen.go` declares `CodeExpired errs.Code = coretoken.CodeExpired` and
twenty-eight siblings. These are **re-exports, not declarations**: the ranges
`0.2.13.*` and `0.3.44.*` stay owned by `internal/core/security/token` — where
every code of the token domain is declared since ADR 0160 — and `0.3.42.*` by
`internal/core/crypto/key/jwk`, and the ADR 0035
ownership audit skips a cross-package selector for exactly this case ("pkg/v1
aliasing an internal sentinel does not make it an owner"). For the same reason
`docs/error-codes.yaml` never lists them: `make error-codes` lists the
constants that declare a code, read from `docs/api`, where a re-export names
the constant it repeats (`init`), so the file keeps one entry per code, under
its owner.

They exist because matching on a code is a stronger contract than matching on a
reason string: a code is a number in `docs/error-codes.yaml`, a reason is a
spelling. A consumer routing an HTTP handler writes
`errs.HasCode(err, token.CodeExpired)` and gets a compile error if the constant
is ever removed.

## Why the JWK loaders are here

`JWK` and `JWKSet` alias `service/crypto/key/jwk.KeyValue` / `Set`, whose fields are
all unexported, so the alias alone publishes a type no consumer can BUILD: every
function that constructs one lives under `internal/`, which Go forbids a
consumer to import. `NewVerifierFromJWK` and `NewSetVerifier` shipped in exactly
that state — taking an argument nothing public could produce — and
`wiring_external_test.go` hid it by importing `internal/service/crypto/key/jwk` to
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

`KeyType` and `Curve` alias `jwk.Type` and `jwk.Curve`, the types `JWK.Kty` and
`JWK.Crv` return, and the seven `KeyType*` / `Curve*` constants are their values.
They are here for the same reason as the loaders (#259): the methods were public
through the `JWK` alias while their result types were not, so a consumer could
compare `Kty()` with a string literal and could not declare a variable, a field
or a parameter of its type. `TestAConsumerCanNameWhatAJWKReports` reads both
from three parsed keys, typed by the published names, and
`TestTheJWKConstantsSpellTheRegisteredValues` holds every constant to its
registered spelling (RFC 7518, RFC 8037).

The whole `token_test` package now imports stdlib and `pkg/v1` only (check:
`cd pkg && GOWORK=off go list -f '{{.XTestImports}}' ./v1/security/token/`), so a JWK
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
  would hand it back. See ADR 0042 and `internal/service/security/token/CLAUDE.md`.
- Do not widen `Issuer` or `Verifier` — they are aliased ports, so ADR 0039
  applies: a sibling interface, never a widening.
- Do not add a constant, a field or a flag that admits `alg: none`.
- Do not hand-edit `README.md`. Edit the package comment in the design
  (`design/security/token.yaml`), run `kit gen`, then `make api` and
  `make docs-readme` (ADR 0167).
- Do not make `Claims` printable, or add an accessor that renders its contents.
  The shape-only rendering is a security property of the core type.
- **Do not import `internal/` from a `_external_test.go` file here.** The
  external test package is the consumer's seat: once it reaches past the facade
  it can no longer notice a facade no consumer can use, which is how the JWK
  defect above shipped.
- Do not add an `UnmarshalJSON`, or any second decoding route, for `JWK` or
  `JWKSet`. `ParseJWK` / `ParseJWKSet` delegate to `jwk.Parse`, the single place
  its checks live — see `internal/service/crypto/key/jwk/CLAUDE.md` §Why there is no
  `UnmarshalJSON`.

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/security/token.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: `doc.go` holds the package comment, which kit writes from the design (ADR 0167), and the hand-written files the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```sh
bazel test --config=race //pkg/v1/security/token:token_test
# Fallback
cd pkg && GOWORK=off go test -race -cover ./v1/security/token/...
```
