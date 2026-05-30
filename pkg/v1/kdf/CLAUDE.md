# pkg/v1/kdf/

## Purpose

Public **key-derivation** facade for KEY SEPARATION (ADR 0013). `Subkey` expands
one strong secret into independent, purpose-bound subkeys. Thin alias +
delegation over `internal/core/crypto`'s Deriver registry — zero runtime cost,
no business logic (per `pkg/` convention).

Importing the package blank-imports `internal/service/crypto/hkdfsha256`,
activating HKDF-SHA256 with **zero non-stdlib deps**.

## Surface

| Identifier | Role |
|---|---|
| `Algorithm` | alias of `corecrypto.Algorithm` (stable scheme id) |
| `HKDFSHA256` | HKDF (RFC 5869) over SHA-256 |
| `Subkey(a, secret, salt, info, length) ([]byte, error)` | derive a subkey; unknown algo → `UnknownKDFAlgorithm`, over-long → `DerivationFailed` |

## NOT for passwords

HKDF assumes a **high-entropy** input — an AEAD key, a Diffie-Hellman shared
secret, an HKDF PRK. It does NOT stretch human passwords (it is fast by design,
so a weak password stays weak). Password hashing/stretching (argon2id) is a
separate, deliberately slow surface. The `info` label provides domain
separation: bind each subkey to its purpose so two derivations from one secret
never collide.

## vs the other crypto surfaces

- **kdf** — split one strong secret into many purpose-bound subkeys.
- **crypto** (`pkg/v1/crypto`) — encrypt/authenticate under a key.
- **sign** (`pkg/v1/sign`) — public-key signatures.
- **hash** (`pkg/v1/hash`) — unkeyed public fingerprints.

## Conventions

- **Aliases, not new types** — `Algorithm = corecrypto.Algorithm`; the const is
  the frozen wire string (`"hkdf-sha256"`).
- **README.md is generated** (`make docs-readme` → gomarkdoc, ADR 0008). Edit the
  package doc comment in `kdf.go`; never hand-edit `README.md`.
- **Signatures freeze at v1.0.0.** New scheme consts can be added; existing ones
  cannot move.

## Do NOT

- Pass a password as the `secret`; HKDF will not protect it. Use the password port.
- Hand-author `README.md` — it is regenerated from the `kdf.go` doc comment.

## Verification

```sh
bazel test --config=race //pkg/v1/kdf:kdf_test
# Fallback
cd pkg/v1 && GOWORK=off go test -race -cover ./kdf/...
```
