# internal/service/crypto/aesgcm/

## Purpose

Registers the **"aes-256-gcm"** AEAD scheme (ADR 0013). Blank-importing the
package — typically via `pkg/v1/crypto` — self-registers the scheme so
`crypto.Seal` / `crypto.Open` resolve. **Stdlib-only** (`crypto/aes` +
`crypto/cipher` + `crypto/rand`): it pulls zero non-stdlib deps, so it is the
default scheme that keeps `pkg/v1/crypto` consumers dep-light. AES-256-GCM is
hardware-accelerated (AES-NI) and FIPS-track.

## Contents

| File | Role |
|---|---|
| `aesgcm.go` | `AEAD` singleton, `aesGCM` (`Algorithm` / `ID` / `Seal` / `Open`), `newGCM` helper |

No `codes.go` / `errors.go` — the scheme returns the shared `core/crypto`
sentinels (`InvalidKey`, `DecryptionFailed`) and wraps a `crypto/rand` fault as
`EntropyFailed`; it mints no codes of its own.

## Wire format

```
[1B Version=0x01][1B algID=0x01][12B nonce][ciphertext || 16B GCM tag]
```

- `Version` comes from `core/crypto.Version` (frozen); `algID` (`0x01`) and the
  12-byte nonce length are frozen for this scheme.
- The nonce is generated from `crypto/rand` inside `Seal`, embedded in the clear
  (standard for GCM), and never reaches a caller.
- `Open` re-validates `Version` + `algID` + length before decrypting.

## Behaviour

- **Seal** — `InvalidKey` if the cipher refuses the key (never happens: `Key` is
  always `KeyLen` bytes), `EntropyFailed` (wrapping the cause) on a
  `crypto/rand` fault. Otherwise returns the self-framed box.
- **Open** — returns the single non-oracle `DecryptionFailed` for **every**
  failure (short/wrong-version/wrong-id framing, bad tag, wrong key, wrong aad).
  It never distinguishes them.
- **Nonce uniqueness** — one fresh random 96-bit nonce per `Seal`. With random
  nonces, AES-GCM's birthday bound caps safe message count per key around 2^32;
  high-volume callers should prefer the (future) XChaCha20-Poly1305 scheme with
  its 192-bit nonce.

## Do NOT

- Differentiate `Open` failures — that builds a padding/format oracle.
- Reuse or expose the nonce; it is internal to the box.
- Log `key.Bytes()`; it is the raw secret handed to `aes.NewCipher`.

## Verification

```sh
bazel test --config=race //internal/service/crypto/aesgcm:aesgcm_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./crypto/aesgcm/...
```
