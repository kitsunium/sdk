<!-- updated: 2026-10-03T03:50:00Z -->
# internal/service/crypto/aead/

## Purpose

The crypto family's authenticated-encryption role (ADR 0155): a key and a
plaintext in, a box out that only the same key opens — the two
`internal/core/crypto` AEAD ports, `AEAD` (whole buffer) and `StreamSealer`
(chunked stream). This directory holds no Go code: it is a prefix, not a
package, and nothing imports `internal/service/crypto/aead` itself. Each member
is a package of the `internal/service` module with its own `CLAUDE.md`.

## The rule that put them here

A package belongs here when it IMPLEMENTS one of those two ports: it registers
a scheme that `Seal` / `SealAs` / `Open` or `SealStream` / `OpenStream`
dispatch to. A composition that only CALLS an AEAD to protect something else
goes where its result is used — `key/keyenvelope` seals a key, so it is a key
representation (`../CLAUDE.md` §The rule that put them together).

## Members

| Package | What it is | Registry key | In `pkg/v1` |
|---|---|---|---|
| `aesgcm/` | AES-256-GCM, the default box scheme, nonce generated and hidden (ADR 0013) | `aes-256-gcm` (wire id `0x01`) | `pkg/v1/crypto` — `Seal` / `Open` / `SealAs` |
| `streamaead/` | AES-256-GCM in 64 KiB authenticated chunks, truncation-resistant (ADR 0014 §D2) | `aes-256-gcm-stream` (stream version `0x02`) | `pkg/v1/crypto` — `SealStream` / `OpenStream` |

## Do NOT

- Put Go code in this directory.
- Add a non-stdlib AEAD here. XChaCha20-Poly1305 lives in
  `third-party/x-crypto/xchacha` and registers through its own blank import
  (ADR 0013), so `pkg/v1/crypto` stays dep-light.
