<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/crypto/agree/

## Purpose

Public **key-agreement** facade (ADR 0014). Two parties exchange public keys and
derive the SAME redacting symmetric `Key` without transmitting it. Thin alias +
delegation over `internal/core/crypto`'s Agreement registry — zero runtime cost,
no business logic (per `pkg/` convention).

Importing the package blank-imports `internal/service/crypto/agree/x25519` AND
`internal/service/crypto/kdf/hkdfsha256` (the KDF `SharedKey` runs the raw secret
through), activating both with **zero non-stdlib deps**.

## Surface

| Identifier | Role |
|---|---|
| `Algorithm` | defined type over `corecrypto.Algorithm` (stable scheme id) — distinct from the other crypto-family `Algorithm` types, so a hash or signature constant does not compile into an agreement call |
| `Key` | alias of `corecrypto.Key` (redacting 256-bit key) |
| `KeyLen` | `corecrypto.KeyLen` — the 32-byte length of every `Key` |
| `X25519` | Diffie-Hellman over Curve25519 (RFC 7748) |
| `NewKey(raw) (Key, error)` | builds a `Key` from exactly `KeyLen` bytes (copied); any other length → `InvalidKey` |
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
- **kdf** (`pkg/v1/crypto/kdf`) — split one strong secret into purpose-bound subkeys.
- **crypto** (`pkg/v1/crypto`) — encrypt/authenticate under a key.
- **sign** (`pkg/v1/crypto/sign`) — public-key signatures.

## Conventions

- **`Key` is an alias, `Algorithm` a defined type** — `Key = corecrypto.Key`;
  `Algorithm` is converted to `corecrypto.Algorithm` at the call into core, so
  the agreement registry stays its own keyspace. The const is the frozen wire
  string (`"x25519"`).
- **README.md is generated** (`make docs-readme` → `tools/genindex` from the
  committed `docs/api`, ADR 0167). The package comment is in `doc.go`, which
  kit writes from the design (`design/crypto.yaml`): edit the design, run
  `kit gen`, then `make api` and `make docs-readme`; never hand-edit `README.md`.
- **Signatures freeze at v1.0.0.** New scheme consts can be added; existing ones
  cannot move.

## Do NOT

- Use the raw DH secret directly — `SharedKey` is the only way out, and it KDFs.
- Log or persist `priv` in the clear; it is the secret half of the keypair.
- Hand-author `README.md` — `make docs-readme` writes it from `docs/api` (ADR 0167).

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/crypto.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the declarations of their own, and `doc.go` — kit's too (ADR 0167) — the package comment. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```sh
bazel test --config=race //pkg/v1/crypto/agree:agree_test
# Fallback
cd pkg/v1 && GOWORK=off go test -race -cover ./crypto/agree/...
```

## Declarations

`decl_gen.go` is written by kit gen from the design (ADR 0170): the declaration of `Algorithm`. Their methods, constructors and helpers stay hand-written, in the files this document names.
