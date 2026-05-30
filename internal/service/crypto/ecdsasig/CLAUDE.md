# internal/service/crypto/ecdsasig/

## Purpose

Registers the **"ecdsa-p256"** signature scheme (ADR 0013). Blank-importing the
package — typically via `pkg/v1/sign` — self-registers the scheme so
`crypto.Sign` / `crypto.Verify` / `crypto.GenerateKey` resolve. **Stdlib-only**
(`crypto/ecdsa` + `crypto/elliptic` + `crypto/sha256` + `crypto/x509` +
`crypto/rand`): zero non-stdlib deps, so it ships in `pkg/v1/sign` alongside the
Ed25519 default.

ECDSA over NIST **P-256** with SHA-256 digests and ASN.1/DER signatures — the
**interoperable** choice (JWT `ES256`, X.509, COSE). Ed25519 (`ed25519sig`) is
the modern default when cross-ecosystem interop is not required.

## Contents

| File | Role |
|---|---|
| `ecdsasig.go` | `Signer` singleton, `ecdsaP256` (`Algorithm`/`GenerateKey`/`Sign`/`Verify`) |

No `codes.go`/`errors.go` — returns the shared `core/crypto` sentinels
(`SigningFailed`) and wraps a `crypto/rand` fault as `KeyGenerationFailed`.

## Key + signature encoding

- **Public key** — PKIX `SubjectPublicKeyInfo` DER (`x509.MarshalPKIXPublicKey`).
- **Private key** — SEC1 EC DER (`x509.MarshalECPrivateKey`).
- **Signature** — ASN.1/DER `(r, s)` (`ecdsa.SignASN1` / `VerifyASN1`).
- **Digest** — ECDSA signs a hash, so `Sign`/`Verify` SHA-256 the message first.

## Behaviour

- **GenerateKey** — P-256 keypair from `crypto/rand` (→ `KeyGenerationFailed` on
  an entropy fault), returned as `(PKIX-pub-DER, SEC1-priv-DER)`.
- **Sign** — parses the SEC1 private key (malformed → `SigningFailed`), SHA-256
  digests the message, emits a DER signature.
- **Verify** — parses the PKIX public key, type-asserts `*ecdsa.PublicKey`, and
  `VerifyASN1`s the digest; any malformed input is `false`, never a panic.

## Do NOT

- Feed an Ed25519/RSA key blob here — the type assertion returns `false`.
- Skip the SHA-256 digest step — ECDSA signs a hash, not the raw message.
- Mint error codes here — they live in `core/crypto`.

## Verification

```sh
bazel test --config=race //internal/service/crypto/ecdsasig:ecdsasig_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./crypto/ecdsasig/...
```
