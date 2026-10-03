<!-- updated: 2026-10-03T03:50:00Z -->
# internal/service/crypto/hash/

## Purpose

The crypto family's digest role (ADR 0155): an unkeyed fingerprint of bytes —
the `internal/core/crypto` `Hasher` port. **Not authentication**: a digest
carries no secret. This directory holds no Go code: it is a prefix, not a
package, and nothing imports `internal/service/crypto/hash` itself — which also
keeps the name from shadowing the standard library's `hash` in any file. Its
member is a package of the `internal/service` module with its own `CLAUDE.md`.

## The rule that put it here

A package belongs here when it registers a `Hasher`. A tag that needs a key is a
`mac/`, and a slow, salted hash of a human password is a `password/`.

## Members

| Package | What it is | Registry keys | In `pkg/v1` |
|---|---|---|---|
| `stdhash/` | the standard library's fingerprint and content-addressing hashers (ADR 0013) | `sha256`, `sha512`, `sha3-256`, `crc32c`, `fnv1a-64` | `pkg/v1/crypto/hash` — `New`, `Sum`, `SumHex`, `NewDigestWriter`, `NewVerifyingReader` |

## Do NOT

- Put Go code in this directory, and above all not a package named `hash`.
- Use a digest where a secret must be proved. That is a `mac/`.
