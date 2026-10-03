<!-- updated: 2026-10-03T03:50:00Z -->
# internal/service/crypto/password/

## Purpose

The crypto family's password role (ADR 0155): a password a PERSON chose —
hashed slowly with a salt for storage, verified, and checked against the most
common ones. The `internal/core/crypto` `PasswordHasher` port, and the list a
new password is checked against. This directory holds no Go code: it is a
prefix, not a package, and nothing imports `internal/service/crypto/password`
itself. Each member is a package of the `internal/service` module with its own
`CLAUDE.md`.

## The rule that put them here

A package belongs here when its subject is a human password: `pbkdf2pw`
registers the `PasswordHasher`; `commonpw` is data — the blocklist NIST SP
800-63B-4 requires a verifier to check — and registers nothing. argon2id, the
memory-hard first choice, is opt-in under `third-party/x-crypto/argon2id`.

## Members

| Package | What it is | Registry key | In `pkg/v1` |
|---|---|---|---|
| `commonpw/` | **data** — the ten thousand most common passwords (SecLists' xato-net top 10 000, MIT, embedded byte for byte at a pinned commit), `IsCommon` case-insensitive (ADR 0143) | *not registered* | `pkg/v1/crypto/password` — `IsCommon` |
| `pbkdf2pw/` | PBKDF2-SHA256 at 600 000 iterations, a PHC string (ADR 0013) | `pbkdf2-sha256` | `pkg/v1/crypto/password` — `Hash`, `Verify`, `NeedsRehash` |

## Do NOT

- Put Go code in this directory.
- Put a fast KDF here: a password stretched by HKDF stays weak, and HKDF is
  `kdf/`'s.
