<!-- updated: 2026-10-03T03:50:00Z -->
# internal/service/crypto/kdf/

## Purpose

The crypto family's key-derivation role (ADR 0155): independent, purpose-bound
subkeys expanded from one STRONG secret — the `internal/core/crypto` `Deriver`
port, and the hierarchy built on it. This directory holds no Go code: it is a
prefix, not a package, and nothing imports `internal/service/crypto/kdf`
itself. Each member is a package of the `internal/service` module with its own
`CLAUDE.md`.

## The rule that put them here

A package belongs here when what its caller receives is a DERIVED key:
`hkdfsha256` registers the `Deriver`, and `keytree` composes it into a
path-addressed tree (`Child("svc").Child("db")`) and registers nothing. HKDF is
fast on purpose, so a human password never comes here — stretching one is
`password/`'s job.

## Members

| Package | What it is | Registry key | In `pkg/v1` |
|---|---|---|---|
| `hkdfsha256/` | HKDF-SHA256 (RFC 5869), key separation and not password stretching (ADR 0013) | `hkdf-sha256` | `pkg/v1/crypto/kdf` — `Subkey` |
| `keytree/` | composition — hierarchical derivation with an injective, length-prefixed path encoding | *not registered* | `pkg/v1/crypto/kdf` — `NewKeyTree` |

## Do NOT

- Put Go code in this directory.
- Feed a password to a member of this directory: a weak secret expanded by HKDF
  stays weak.
