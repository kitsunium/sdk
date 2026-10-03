<!-- updated: 2026-10-03T12:00:00Z -->
# internal/core/crypto/key/

## Purpose

The core mirror of `internal/service/crypto/key/` (ADR 0155 §2, ADR 0160): the
crypto family's key-representation role, at the same path in the core as in
the service. This directory holds no Go code: it is a prefix, not a package,
and nothing imports `internal/core/crypto/key` itself.

## Members

| Package | What it holds | Code range | Its service |
|---|---|---|---|
| `jwk/` | the JSON Web Key format's codes and sentinels — nothing else | `0.3.42.*` | `internal/service/crypto/key/jwk` |

`keyenvelope` has no member here: it declares no code of its own (it returns
the core sentinel `0.2.4.19`, `InvalidKeyEnvelope`, from `internal/core/crypto`)
and implements no port, so ADR 0160 gives it nothing to mirror.

## Do NOT

- Put Go code in this directory.
- Add a member that only re-exports what `internal/core/crypto` already
  declares. A member exists where a service package under `crypto/key/` has
  codes, values or ports of its own.
