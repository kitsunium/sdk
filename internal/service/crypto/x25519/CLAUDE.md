# internal/service/crypto/x25519/

## Purpose

Registers the **"x25519"** key-agreement scheme (ADR 0014). Blank-importing the
package — typically via `pkg/v1/agree` — self-registers the scheme so
`crypto.GenerateAgreementKey` / `crypto.AgreementShared` resolve. **Stdlib-only**
(`crypto/ecdh` + `crypto/rand`): it pulls zero non-stdlib deps, so it is the
default Diffie-Hellman scheme that keeps `pkg/v1/agree` consumers dep-light.

X25519 (RFC 7748) is the modern DH default: 32-byte public, private, and shared
values. `crypto/ecdh` rejects low-order peer points, so a malformed or
attacker-chosen peer key surfaces as a typed error, never a degenerate secret.

## Contents

| File | Role |
|---|---|
| `x25519.go` | `Agreement` singleton, `x25519Agreement` (`Algorithm` / `GenerateKey` / `Shared`) |

No `codes.go` / `errors.go` — the scheme returns the shared `core/crypto`
sentinels: a `crypto/rand` fault wraps as `KeyGenerationFailed`, and a rejected
peer point flows up to the dispatcher which wraps it as `AgreementFailed`
(leaking no key bytes). It mints no codes of its own, so it adds **no**
`audit_srcs` to the root filegroup.

## Key-hygiene contract

- **`priv` (from `GenerateKey`) is secret material.** Hold it like a password,
  never log it, and `Zeroize` it when done.
- **`secret` (from `Shared`) is RAW DH output, NOT a key.** It MUST be run
  through a KDF before any use — the `pkg/v1/agree` facade `SharedKey` does
  exactly this (HKDF-SHA256) and never hands the raw secret back. Zeroize it too.

## Behaviour

- **GenerateKey** — `ecdh.X25519().GenerateKey(rand.Reader)`; `KeyGenerationFailed`
  (wrapping the cause) on an entropy fault, otherwise raw 32-byte `(pub, priv)`.
- **Shared** — rebuilds the local private key and the peer public key (rejecting
  low-order/garbage points), then `priv.ECDH(peerPub)`. A bad input returns a
  non-nil error that the dispatcher wraps as `AgreementFailed`.

## Do NOT

- Return or use the raw `Shared` secret as a key without a KDF — always KDF it.
- Log `priv` or `secret`; they are raw key material.
- Mint error codes here — entropy + agreement-failure errors live in `core/crypto`.

## Verification

```sh
bazel test --config=race //internal/service/crypto/x25519:x25519_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./crypto/x25519/...
```
