<!-- updated: 2026-10-03T03:50:00Z -->
# internal/service/crypto/sign/

## Purpose

The crypto family's signature role (ADR 0155): signed with a private key,
verified by anybody holding the public key — the `internal/core/crypto`
`Signer` port. This directory holds no Go code: it is a prefix, not a package,
and nothing imports `internal/service/crypto/sign` itself. Each member is a
package of the `internal/service` module with its own `CLAUDE.md`.

## The rule that put them here

A package belongs here when it registers a `Signer` scheme. A tag only the key's
holder can check is a `mac/`.

## Members

| Package | What it is | Registry key | In `pkg/v1` |
|---|---|---|---|
| `ecdsasig/` | ECDSA P-256 with SHA-256 and ASN.1/DER signatures — the interoperable choice (JWT `ES256`, X.509, COSE) (ADR 0013) | `ecdsa-p256` | `pkg/v1/crypto/sign` — `ECDSAP256` |
| `ed25519sig/` | Ed25519, the modern default — fixed sizes, no parameter to misconfigure (ADR 0013) | `ed25519` | `pkg/v1/crypto/sign` — `Ed25519` |

## Do NOT

- Put Go code in this directory.
- Re-encode a signature for one wire format here. The JOSE fixed-width `R||S`
  form a JWT needs is built in `internal/service/token`, because DER is the
  port's contract.
