# pkg/v1/agree/

## Purpose

Public **key-agreement** facade (ADR 0014). Two parties exchange public keys and
derive the SAME redacting symmetric `Key` without transmitting it. Thin alias +
delegation over `internal/core/crypto`'s Agreement registry — zero runtime cost,
no business logic (per `pkg/` convention).

Importing the package blank-imports `internal/service/crypto/x25519` AND
`internal/service/crypto/hkdfsha256` (the KDF `SharedKey` runs the raw secret
through), activating both with **zero non-stdlib deps**.

## Surface

| Identifier | Role |
|---|---|
| `Algorithm` | alias of `corecrypto.Algorithm` (stable scheme id) |
| `Key` | alias of `corecrypto.Key` (redacting 256-bit key) |
| `X25519` | Diffie-Hellman over Curve25519 (RFC 7748) |
| `GenerateKey(a) (pub, priv []byte, error)` | fresh keypair; `priv` is secret |
| `SharedKey(a, priv, peerPub, info) (Key, error)` | HKDF'd shared key; low-order peer → `AgreementFailed` |

## The raw DH secret is never handed back

A raw Diffie-Hellman secret is biased key material, unsafe to use directly.
`SharedKey` runs it through HKDF-SHA256 (bound to `info` for domain separation)
and returns a redacting `Key` — the raw secret never leaves the SDK and is
zeroized after the KDF consumes it. Two applications sharing one keypair derive
independent keys by passing different `info` labels.

## Key hygiene

- **`priv` (from `GenerateKey`)** is secret material: hold it like a password,
  never log it, zero it when done.
- **The returned `Key`** redacts in logs; call its `Zeroize` when finished.

## vs the other crypto surfaces

- **agree** — establish a *shared* key between two parties from their keypairs.
- **kdf** (`pkg/v1/kdf`) — split one strong secret into purpose-bound subkeys.
- **crypto** (`pkg/v1/crypto`) — encrypt/authenticate under a key.
- **sign** (`pkg/v1/sign`) — public-key signatures.

## Conventions

- **Aliases, not new types** — `Algorithm`/`Key = corecrypto.*`; the const is the
  frozen wire string (`"x25519"`).
- **README.md is generated** (`make docs-readme` → gomarkdoc, ADR 0008). Edit the
  package doc comment in `agree.go`; never hand-edit `README.md`.
- **Signatures freeze at v1.0.0.** New scheme consts can be added; existing ones
  cannot move.

## Do NOT

- Use the raw DH secret directly — `SharedKey` is the only way out, and it KDFs.
- Log or persist `priv` in the clear; it is the secret half of the keypair.
- Hand-author `README.md` — it is regenerated from the `agree.go` doc comment.

## Verification

```sh
bazel test --config=race //pkg/v1/agree:agree_test
# Fallback
cd pkg/v1 && GOWORK=off go test -race -cover ./agree/...
```
