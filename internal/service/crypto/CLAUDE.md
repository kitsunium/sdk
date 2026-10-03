<!-- updated: 2026-10-03T03:45:00Z -->
# internal/service/crypto/

## Purpose

The concrete crypto schemes behind the eight `internal/core/crypto` ports, plus
the compositions and the key format built on top of them, grouped by the ROLE
each one plays (ADR 0155). Every package here is **stdlib-only** — the SDK's
non-stdlib crypto (Argon2id, XChaCha20-Poly1305) lives in
`third-party/x-crypto/*` and is opt-in (ADR 0013 / ADR 0014).

Most packages **self-register** at import through a package-level `var`, never
an `init()`, so a blank import from the matching `pkg/v1/crypto` facade is all
it takes to make the core dispatch resolve. Four do not register anything:
`keyenvelope` and `keytree` are compositions over already-registered schemes,
`jwk` is a wire format rather than an algorithm, and `commonpw` is data — a
list a password is checked against, with its source and licence beside it.

## The rule that put them together

This directory and its eight role directories hold no Go code: they are
prefixes, not packages, and nothing imports `internal/service/crypto` or
`internal/service/crypto/sign` itself. A role names the job a package does for
its caller — which is the core port it implements, when it implements one, and
the child of `pkg/v1/crypto` that publishes it:

| Role | Port of `internal/core/crypto` | Published by | Members |
|---|---|---|---|
| `aead/` | `AEAD`, `StreamSealer` | `pkg/v1/crypto` (the family's root) | `aesgcm`, `streamaead` |
| `agree/` | `Agreement` | `pkg/v1/crypto/agree` | `x25519` |
| `hash/` | `Hasher` | `pkg/v1/crypto/hash` | `stdhash` |
| `kdf/` | `Deriver` | `pkg/v1/crypto/kdf` | `hkdfsha256`, `keytree` |
| `key/` | none — a key's representation, not an algorithm | `pkg/v1/crypto` (`WrapKey`), `pkg/v1/security/token` (`JWK`) | `jwk`, `keyenvelope` |
| `mac/` | `MAC` | `pkg/v1/crypto/mac` | `hmacsha2` |
| `password/` | `PasswordHasher` | `pkg/v1/crypto/password` | `commonpw`, `pbkdf2pw` |
| `sign/` | `Signer` | `pkg/v1/crypto/sign` | `ecdsasig`, `ed25519sig` |

A composition goes where its RESULT is used, not where its parts come from:
`keytree` composes HKDF and hands out derived keys, so it is a `kdf`;
`keyenvelope` composes PBKDF2 and AES-GCM and hands out a key sealed at rest, so
it is a `key`, beside `jwk`, the same key written on the wire. `commonpw` is the
list a new password is checked against, so it is a `password`. Each role
directory carries a `CLAUDE.md` naming its members and this rule.

## Contents

| Package | Port / role | Registry key | Facade | Code range |
|---|---|---|---|---|
| `aead/aesgcm/` | `AEAD` — authenticated encryption, hidden nonce | `aes-256-gcm` (wire id `0x01`) | `pkg/v1/crypto` | core `0.2.4.*` |
| `aead/streamaead/` | `StreamSealer` — chunked streaming AEAD | `aes-256-gcm-stream` | `pkg/v1/crypto` | core `0.2.4.*` |
| `agree/x25519/` | `Agreement` — DH-style shared secret | `x25519` | `pkg/v1/crypto/agree` | core `0.2.4.*` |
| `hash/stdhash/` | `Hasher` — unkeyed fingerprints | `sha256`, `sha512`, `sha3-256`, `crc32c`, `fnv1a-64` | `pkg/v1/crypto/hash` | core `0.2.4.*` |
| `kdf/hkdfsha256/` | `Deriver` — key-separation KDF | `hkdf-sha256` | `pkg/v1/crypto/kdf` | core `0.2.4.*` |
| `kdf/keytree/` | composition — path-addressed hierarchical derivation (HKDF) | *not registered* | `pkg/v1/crypto/kdf` | core `0.2.4.*` |
| `key/jwk/` | **format** — RFC 7517 JWK / JWK Set for EC, OKP and oct keys | *not registered* | `pkg/v1/security/token` (`JWK`, `JWKSet`) | **`0.3.42.*`** |
| `key/keyenvelope/` | composition — password-wrapped DEK at rest (AEAD + PBKDF2) | *not registered* | `pkg/v1/crypto` | core `0.2.4.19` |
| `mac/hmacsha2/` | `MAC` — keyed detached authentication | `hmac-sha256` | `pkg/v1/crypto/mac` | core `0.2.4.*` |
| `password/commonpw/` | **data** — the ten thousand most common passwords (SecLists' xato-net top 10 000, MIT, embedded byte for byte at a pinned commit), `IsCommon` case-insensitive — ADR 0143 | *not registered* | `pkg/v1/crypto/password` (`IsCommon`) | none |
| `password/pbkdf2pw/` | `PasswordHasher` — slow, salted, PHC string | `pbkdf2-sha256` | `pkg/v1/crypto/password` | core `0.2.4.*` |
| `sign/ecdsasig/` | `Signer` — ECDSA P-256, SHA-256, ASN.1/DER | `ecdsa-p256` | `pkg/v1/crypto/sign` | core `0.2.4.*` |
| `sign/ed25519sig/` | `Signer` — Ed25519, the modern default | `ed25519` | `pkg/v1/crypto/sign` | core `0.2.4.*` |

`jwk` is the only package in this subtree that owns a `PP` slot: every scheme
routes through a core port and therefore emits the shared `core/crypto`
sentinels, whereas a key format has rejections of its own (malformed document,
unsupported `kty`, off-curve point, ambiguous `kid`) that no port models. Its
range `0x00_03_2A_00` kept its value when the package moved to `key/jwk`; only
the owner path in `codeRangeOwners` followed it (ADR 0160).

## Conventions

- **Registration is a package-level `var`, never `init()`** —
  `var Signer = corecrypto.RegisterSigner(ecdsaP256{})`. A distinct scheme
  claiming a taken `Algorithm` or wire id panics at boot with the dotted-quad
  code.
- **Schemes mint no error codes.** They return the shared `core/crypto`
  sentinels (`SigningFailed`, `DecryptionFailed`, …) or wrap a `crypto/rand`
  fault as `KeyGenerationFailed`, `EntropyFailed` or `PasswordHashFailed`.
  `jwk` is the documented exception.
- **Secrets stay redacting.** `core/crypto.Key` answers `<redacted>` under
  `%v` / `%s` / `%#v`, and anything here that holds or re-wraps key material
  keeps that property — including `jwk.KeyValue`, whose whole export surface is
  designed around not undoing it.
- **`Open` and `Verify` are non-oracle.** Every decryption failure collapses to
  one sentinel; tag comparison goes through `hmac.Equal`, never `==`.

## Do NOT

- Add a non-stdlib crypto dependency here. It belongs in
  `third-party/x-crypto/*`, opt-in (ADR 0013).
- Mint an error code in a scheme package — they live in `core/crypto`.
- Add remote key retrieval (JWKS over HTTP, KMS clients) to `jwk` or anywhere
  else in this subtree. Fetching is a connector concern, not a crypto one — see
  `key/jwk/CLAUDE.md` §Do NOT.
- Differentiate a decryption failure, or compare a MAC tag with `==`.
- Put Go code in this directory or in a role directory: it would make the
  family, or the role, a package nobody chose.
- File a package by the algorithm it USES. `keyenvelope` uses PBKDF2 and is
  not a `password`; ask what its caller receives.

## Subtree

Each package documents its own contract, encodings and refusals:
`aead/aesgcm/`, `aead/streamaead/`, `agree/x25519/`, `hash/stdhash/`,
`kdf/hkdfsha256/`, `kdf/keytree/`, `key/jwk/`, `key/keyenvelope/`,
`mac/hmacsha2/`, `password/commonpw/`, `password/pbkdf2pw/`,
`sign/ecdsasig/`, `sign/ed25519sig/`. `password/commonpw/` also records the
source, the pinned commit, the digest and the licence of the list it embeds.
Each role directory's `CLAUDE.md` lists its members.

## Verification

```sh
# Primary (Bazel)
bazel test --config=race //internal/service/crypto/...

# Fallback (go test)
cd internal/service && GOWORK=off go test -race -cover ./crypto/...
```
