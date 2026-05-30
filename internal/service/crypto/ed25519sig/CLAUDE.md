# internal/service/crypto/ed25519sig/

## Purpose

Registers the **"ed25519"** signature scheme (ADR 0013). Blank-importing the
package — typically via `pkg/v1/sign` — self-registers the scheme so
`crypto.Sign` / `crypto.Verify` / `crypto.GenerateKey` resolve. **Stdlib-only**
(`crypto/ed25519` + `crypto/rand`): it pulls zero non-stdlib deps, so it is the
default signature scheme that keeps `pkg/v1/sign` consumers dep-light.

Ed25519 is the modern default: fixed-size keys (32-byte public, 64-byte
private), 64-byte signatures, fast constant-time verification, and no parameter
choices to misconfigure. ECDSA lands later under its own scheme package
(`third-party/x-crypto/*` or a sibling), exactly as XChaCha20 followed aesgcm.

## Contents

| File | Role |
|---|---|
| `ed25519sig.go` | `Signer` singleton, `ed25519Signer` (`Algorithm` / `GenerateKey` / `Sign` / `Verify`) |

No `codes.go` / `errors.go` — the scheme returns the shared `core/crypto`
sentinels (`SigningFailed`) and wraps a `crypto/rand` fault as
`KeyGenerationFailed`; it mints no codes of its own.

## Behaviour

- **GenerateKey** — `KeyGenerationFailed` (wrapping the cause) on a `crypto/rand`
  fault; otherwise a fresh `(pub 32B, priv 64B)` keypair.
- **Sign** — guards `len(priv) == ed25519.PrivateKeySize` and returns the typed
  `SigningFailed` for a malformed key, because `ed25519.Sign` **panics** on a
  bad key length. A valid key yields a deterministic 64-byte signature.
- **Verify** — guards `len(pub) == ed25519.PublicKeySize` (returns `false`,
  since `ed25519.Verify` **panics** on a bad public-key length); a wrong-length
  signature returns `false` without a panic. Constant-time w.r.t. the signature.

## Do NOT

- Log the private key bytes; treat them like `Key.Bytes()` — raw secret material.
- Let a malformed key reach `ed25519.Sign`/`ed25519.Verify` unguarded; they panic.
- Mint error codes here — dispatch + key/entropy errors live in `core/crypto`.

## Verification

```sh
bazel test --config=race //internal/service/crypto/ed25519sig:ed25519sig_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./crypto/ed25519sig/...
```
