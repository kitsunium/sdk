<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/crypto/kdf/

## Purpose

Public **key-derivation** facade for KEY SEPARATION (ADR 0013). `Subkey` expands
one strong secret into independent, purpose-bound subkeys. Thin alias +
delegation over `internal/core/crypto`'s Deriver registry — zero runtime cost,
no business logic (per `pkg/` convention).

Importing the package blank-imports `internal/service/crypto/kdf/hkdfsha256`,
activating HKDF-SHA256 with **zero non-stdlib deps**. `KeyTree` re-exports the
path-addressed derivation of `internal/service/crypto/kdf/keytree` (ADR 0014 §D3).

## Surface

| Identifier | Role |
|---|---|
| `Algorithm` | defined type over `corecrypto.Algorithm` (stable scheme id) — a hash or MAC constant does not compile into a KDF call |
| `HKDFSHA256` | HKDF (RFC 5869) over SHA-256 |
| `Subkey(a, secret, salt, info, length) ([]byte, error)` | derive a subkey; unknown algo → `UnknownKDFAlgorithm`, over-long → `DerivationFailed` |
| `Key` / `KeyLen` | alias of `corecrypto.Key` (redacting 256-bit key) and its 32-byte length |
| `NewKey(raw) (Key, error)` | builds a `Key` from exactly `KeyLen` bytes (copied); any other length → `InvalidKey` |
| `KeyTree` | alias of `keytree.KeyTree` — an immutable node; `Child(segment)` descends one length-prefixed segment, `DeriveKey()` re-derives a 32-byte `Key` from the master at that path |
| `NewKeyTree(algo, master) KeyTree` | the root node; `master` is shared by reference, so zeroizing it invalidates every derived node |

## NOT for passwords

HKDF assumes a **high-entropy** input — an AEAD key, a Diffie-Hellman shared
secret, an HKDF PRK. It does NOT stretch human passwords (it is fast by design,
so a weak password stays weak). Password hashing/stretching is a separate,
deliberately slow surface (`pkg/v1/crypto/password`: PBKDF2-SHA256, argon2id opt-in
under `third-party/x-crypto/argon2id`). The `info` label provides domain
separation: bind each subkey to its purpose so two derivations from one secret
never collide.

## vs the other crypto surfaces

- **kdf** — split one strong secret into many purpose-bound subkeys.
- **crypto** (`pkg/v1/crypto`) — encrypt/authenticate under a key.
- **sign** (`pkg/v1/crypto/sign`) — public-key signatures.
- **hash** (`pkg/v1/crypto/hash`) — unkeyed public fingerprints.

## Conventions

- **`Key` / `KeyTree` are aliases, `Algorithm` a defined type** — `Algorithm`
  is converted to `corecrypto.Algorithm` at the call, so the KDF registry stays
  its own keyspace. The const is the frozen wire string (`"hkdf-sha256"`).
- **README.md is generated** (`make docs-readme` → gomarkdoc, ADR 0008). Edit the
  package doc comment in `kdf.go`; never hand-edit `README.md`.
- **Signatures freeze at v1.0.0.** New scheme consts can be added; existing ones
  cannot move.

## Do NOT

- Pass a password as the `secret`; HKDF will not protect it. Use the password port.
- Hand-author `README.md` — it is regenerated from the `kdf.go` doc comment.

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/crypto.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the package comment and the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```sh
bazel test --config=race //pkg/v1/crypto/kdf:kdf_test
# Fallback
cd pkg/v1 && GOWORK=off go test -race -cover ./crypto/kdf/...
```
