<!-- updated: 2026-10-03T03:50:00Z -->
# internal/service/crypto/key/

## Purpose

The crypto family's key-representation role (ADR 0155): a key sealed at rest
under a passphrase, or a key written on the wire as a JSON Web Key. Neither is
an algorithm, and neither implements an `internal/core/crypto` port. This
directory holds no Go code: it is a prefix, not a package, and nothing imports
`internal/service/crypto/key` itself. Each member is a package of the
`internal/service` module with its own `CLAUDE.md`.

## The rule that put them here

A package belongs here when what its caller receives is a KEY in a form that
leaves the process: `keyenvelope` turns a data-encryption key into a `$kenv$`
string under a PBKDF2-derived key-encryption key, sealed with AES-256-GCM;
`jwk` reads and writes RFC 7517 JWKs and JWK Sets for EC, OKP and oct keys.
They are filed by what they produce, not by the algorithms they call:
`keyenvelope` uses PBKDF2 and is not a `password/`, and uses AES-GCM and is not
an `aead/`.

## Members

| Package | What it is | Code range | In `pkg/v1` |
|---|---|---|---|
| `jwk/` | **format** — RFC 7517 JWK / JWK Set, private export opt-in and never the default | **`0x00_03_2A_00`** (`0.3.42.*`), the only range in the crypto subtree | `pkg/v1/token` — `JWK`, `JWKSet` |
| `keyenvelope/` | composition — a password-wrapped DEK at rest, a frozen wire grammar (ADR 0014 §D3) | core sentinel `0.2.4.19` | `pkg/v1/crypto` — `WrapKey` / `UnwrapKey` |

`jwk`'s range kept its value when the package moved here; only the owner path in
`codeRangeOwners` followed it (ADR 0160).

## Do NOT

- Put Go code in this directory.
- Fetch keys here — JWKS over HTTP, a KMS client. Fetching is a connector
  concern, not a crypto one (`jwk/CLAUDE.md` §Do NOT).
