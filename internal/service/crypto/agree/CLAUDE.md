<!-- updated: 2026-10-03T03:50:00Z -->
# internal/service/crypto/agree/

## Purpose

The crypto family's key-agreement role (ADR 0155): two parties derive one
shared key from their own private key and the other's public key — the
`internal/core/crypto` `Agreement` port. This directory holds no Go code: it is
a prefix, not a package, and nothing imports `internal/service/crypto/agree`
itself. Its member is a package of the `internal/service` module with its own
`CLAUDE.md`.

## The rule that put it here

A package belongs here when it registers an `Agreement` scheme. The family has
one member; a second Diffie-Hellman scheme joins it here.

## Members

| Package | What it is | Registry key | In `pkg/v1` |
|---|---|---|---|
| `x25519/` | X25519 (RFC 7748) over `crypto/ecdh`, a low-order peer point refused as a typed error (ADR 0014) | `x25519` | `pkg/v1/crypto/agree` — `GenerateKey`, `SharedKey` |

## Do NOT

- Put Go code in this directory.
- Hand back a raw shared secret. The port returns a key that went through a KDF
  (`SharedKey` is agreement + HKDF), and a member that skipped it would undo the
  domain's rule (`internal/core/crypto/CLAUDE.md`).
