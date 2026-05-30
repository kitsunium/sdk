# internal/service/crypto/hkdfsha256/

## Purpose

Registers the **"hkdf-sha256"** key-derivation scheme (ADR 0013). Blank-importing
the package — typically via `pkg/v1/kdf` — self-registers the scheme so
`crypto.Subkey` resolves. **Stdlib-only** (`crypto/hkdf` + `crypto/sha256`, both
Go 1.26): it pulls zero non-stdlib deps, so it is the default KDF that keeps
`pkg/v1/kdf` consumers dep-light.

HKDF (RFC 5869) is for **key separation** — expanding one strong secret into
independent, purpose-bound subkeys — **NOT** password stretching. It is fast by
design; a weak password fed to it stays weak. Password hashing (argon2id) is a
separate, deliberately slow port.

## Contents

| File | Role |
|---|---|
| `hkdfsha256.go` | `Deriver` singleton, `hkdfSHA256` (`Algorithm` / `Derive`) |

No `codes.go` / `errors.go` — the scheme returns the shared `core/crypto`
sentinel `DerivationFailed`; it mints no codes of its own.

## Behaviour

- **Derive** — single-call `hkdf.Key(sha256.New, secret, salt, info, length)`.
  `salt` may be nil (HKDF substitutes a zero salt). The only realistic failure
  is `length > 255*32 = 8160` bytes, mapped to the typed `DerivationFailed`.
- **Key separation** — the `info` label binds each subkey to its purpose, so
  `Derive(s, salt, "aead-key", 32)` and `Derive(s, salt, "mac-key", 32)` are
  cryptographically independent.

## Do NOT

- Feed a human password here; HKDF does not stretch — use the (future) argon2id
  password port for that.
- Mint error codes here — derivation + dispatch errors live in `core/crypto`.

## Verification

```sh
bazel test --config=race //internal/service/crypto/hkdfsha256:hkdfsha256_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./crypto/hkdfsha256/...
```
