# internal/service/crypto/

## Purpose

The concrete crypto schemes behind the eight `internal/core/crypto` ports, plus
the compositions and the key format built on top of them. Every package here is
**stdlib-only** — the SDK's non-stdlib crypto (Argon2id, XChaCha20-Poly1305)
lives in `third-party/x-crypto/*` and is opt-in (ADR 0013 / ADR 0014).

Most packages **self-register** at import through a package-level `var`, never
an `init()`, so a blank import from the matching `pkg/v1/*` facade is all it
takes to make the core dispatch resolve. Three do not register anything:
`keyenvelope` and `keytree` are compositions over already-registered schemes,
and `jwk` is a wire format rather than an algorithm.

## Contents

| Package | Port / role | Registry key | Facade | Code range |
|---|---|---|---|---|
| `aesgcm/` | `AEAD` — authenticated encryption, hidden nonce | `aes-256-gcm` (wire id `0x01`) | `pkg/v1/crypto` | core `0.2.4.*` |
| `streamaead/` | `StreamSealer` — chunked streaming AEAD | `aes-256-gcm-stream` | `pkg/v1/crypto` | core `0.2.4.*` |
| `stdhash/` | `Hasher` — unkeyed fingerprints | `sha256`, `sha512`, `sha3-256`, `crc32c`, `fnv1a-64` | `pkg/v1/hash` | core `0.2.4.*` |
| `hmacsha2/` | `MAC` — keyed detached authentication | `hmac-sha256` | `pkg/v1/mac` | core `0.2.4.*` |
| `ecdsasig/` | `Signer` — ECDSA P-256, SHA-256, ASN.1/DER | `ecdsa-p256` | `pkg/v1/sign` | core `0.2.4.*` |
| `ed25519sig/` | `Signer` — Ed25519, the modern default | `ed25519` | `pkg/v1/sign` | core `0.2.4.*` |
| `hkdfsha256/` | `Deriver` — key-separation KDF | `hkdf-sha256` | `pkg/v1/kdf` | core `0.2.4.*` |
| `pbkdf2pw/` | `PasswordHasher` — slow, salted, PHC string | `pbkdf2-sha256` | `pkg/v1/password` | core `0.2.4.*` |
| `x25519/` | `Agreement` — DH-style shared secret | `x25519` | `pkg/v1/agree` | core `0.2.4.*` |
| `keyenvelope/` | composition — password-wrapped DEK at rest (AEAD + PBKDF2) | *not registered* | `pkg/v1/crypto` | core `0.2.4.19` |
| `keytree/` | composition — path-addressed hierarchical derivation (HKDF) | *not registered* | `pkg/v1/kdf` | core `0.2.4.*` |
| `jwk/` | **format** — RFC 7517 JWK / JWK Set for EC, OKP and oct keys | *not registered* | *internal only today* | **`0.3.42.*`** |

`jwk` is the only package in this subtree that owns a `PP` slot: every scheme
routes through a core port and therefore emits the shared `core/crypto`
sentinels, whereas a key format has rejections of its own (malformed document,
unsupported `kty`, off-curve point, ambiguous `kid`) that no port models.

## Conventions

- **Registration is a package-level `var`, never `init()`** —
  `var Signer = corecrypto.RegisterSigner(ecdsaP256{})`. A distinct scheme
  claiming a taken `Algorithm` or wire id panics at boot with the dotted-quad
  code.
- **Schemes mint no error codes.** They return the shared `core/crypto`
  sentinels (`SigningFailed`, `DecryptionFailed`, …) or wrap a `crypto/rand`
  fault as `KeyGenerationFailed`. `jwk` is the documented exception.
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
  `jwk/CLAUDE.md` §Do NOT.
- Differentiate a decryption failure, or compare a MAC tag with `==`.

## Subtree

Each package documents its own contract, encodings and refusals:
`aesgcm/`, `ecdsasig/`, `ed25519sig/`, `hkdfsha256/`, `hmacsha2/`, `jwk/`,
`keyenvelope/`, `keytree/`, `pbkdf2pw/`, `stdhash/`, `streamaead/`, `x25519/`.

## Verification

```sh
# Primary (Bazel)
bazel test --config=race //internal/service/crypto/...

# Fallback (go test)
cd internal/service && GOWORK=off go test -race -cover ./crypto/...
```
