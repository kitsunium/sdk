<!-- updated: 2026-10-03T03:50:00Z -->
# internal/service/crypto/mac/

## Purpose

The crypto family's message-authentication role (ADR 0155): a keyed, detached
tag only a holder of the key can produce — the `internal/core/crypto` `MAC`
port. This directory holds no Go code: it is a prefix, not a package, and
nothing imports `internal/service/crypto/mac` itself. Its member is a package
of the `internal/service` module with its own `CLAUDE.md`.

## The rule that put it here

A package belongs here when it registers a `MAC` scheme. A digest without a key
is a `hash/`; a signature anybody holding the public key can verify is a
`sign/`.

## Members

| Package | What it is | Registry key | In `pkg/v1` |
|---|---|---|---|
| `hmacsha2/` | HMAC-SHA256 (RFC 2104), verified through `hmac.Equal` in constant time (ADR 0014) | `hmac-sha256` | `pkg/v1/crypto/mac` — `Tag`, `Verify` |

## Do NOT

- Put Go code in this directory.
- Compare a tag with `==` or `bytes.Equal`.
