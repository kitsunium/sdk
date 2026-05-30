# pkg/v1/hash/

## Purpose

Public **fingerprint / content-addressing** facade (ADR 0013). One verb, many
algorithms: `Sum` / `SumHex` hash arbitrary bytes to a digest; `New` returns a
streaming `hash.Hash` for `io.Copy` over large inputs. Thin alias + delegation
over `internal/core/crypto`'s Hasher registry — zero runtime cost, no business
logic (per `pkg/` convention).

Importing the package blank-imports `internal/service/crypto/stdhash`, activating
all five stdlib hashers with **zero non-stdlib deps**.

## Surface

| Identifier | Role |
|---|---|
| `Algorithm` | alias of `corecrypto.Algorithm` (stable scheme id) |
| `SHA256` / `SHA512` / `SHA3256` | cryptographic-strength digest consts |
| `CRC32C` / `FNV1a64` | fast **non**-cryptographic checksum/fingerprint consts |
| `Sum(a, data) ([]byte, error)` | one-shot digest; unknown algo → `UnknownHashAlgorithm` |
| `SumHex(a, data) (string, error)` | `Sum` as canonical lowercase hex (frozen string form) |
| `New(a) (hash.Hash, error)` | streaming hash for `io.Copy` |

## NOT authentication

This is the public-digest surface: content IDs, cache keys, dedup keys — NOT
message authentication, password storage, or signatures. No hasher is keyed and
a digest is public. The package deliberately offers **no equality helper**:
never branch on a secret-dependent comparison of a digest. Keyed integrity lives
on the AEAD (`pkg/v1/crypto`) and (future) signature surfaces, constant-time by
construction there.

## Conventions

- **Aliases, not new types** — `Algorithm = corecrypto.Algorithm`; the consts
  are the frozen wire strings (`"sha256"`, `"crc32c"`, …).
- **README.md is generated** (`make docs-readme` → gomarkdoc, ADR 0008). Edit the
  package doc comment in `hash.go`; never hand-edit `README.md`.
- **Signatures freeze at v1.0.0.** New algorithm consts can be added; existing
  ones cannot move.

## Do NOT

- Add an `Equal`/`Verify` helper that compares digests — it invites
  secret-dependent branching. Authentication belongs to the keyed surfaces.
- Use `CRC32C` / `FNV1a64` where collision resistance matters; they are fast,
  not secure.
- Hand-author `README.md` — it is regenerated from the `hash.go` doc comment.

## Verification

```sh
bazel test --config=race //pkg/v1/hash:hash_test
# Fallback
cd pkg/v1 && GOWORK=off go test -race -cover ./hash/...
```
