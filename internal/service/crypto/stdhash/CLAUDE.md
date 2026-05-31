# internal/service/crypto/stdhash/

## Purpose

Registers the **stdlib fingerprint / content-addressing hashers** (ADR 0013):
`sha256`, `sha512`, `sha3-256`, `crc32c`, `fnv1a-64`. Blank-importing the
package — typically via `pkg/v1/hash` — self-registers every scheme so
`crypto.Sum` / `crypto.SumHex` / `crypto.NewHash` resolve. **Stdlib-only**
(`crypto/sha256`, `crypto/sha512`, `crypto/sha3`, `hash/crc32`, `hash/fnv`): it
pulls zero non-stdlib deps, keeping `pkg/v1/hash` consumers dep-light.

Peer of `internal/service/crypto/aesgcm` but on the **Hasher** port, not the
AEAD port: these are **NOT authentication**. No hasher is keyed and a digest is
public. Keyed integrity belongs to the AEAD / signature ports.

## Contents

| File | Role |
|---|---|
| `stdhash.go` | package doc only |
| `sha256_hasher.go` / `sha512_hasher.go` / `sha3_hasher.go` / `crc32c_hasher.go` / `fnv_hasher.go` | one empty-struct `Hasher` singleton per file (`Algorithm` / `New`), each self-registering via a package-level `var _ = corecrypto.RegisterHasher(…)`; `crc32c_hasher.go` also owns the shared `castagnoli` CRC-32C table |
| `digest_writer.go` | `DigestWriter` — tees `Write` into a destination + a running hash; `Sum` / `SumHex` over everything written (`NewDigestWriter`) |
| `verifying_reader.go` | `VerifyingReader` — hashes a stream and verifies it against an expected hex digest on the final (EOF) read only, surfacing the core `DigestMismatch` sentinel; never fails mid-stream (`NewVerifyingReader`) |

No `codes.go` / `errors.go` — dispatch errors (`UnknownHashAlgorithm`) and the
`DigestMismatch` sentinel are minted by `core/crypto`; the `Hasher` schemes only
construct stdlib `hash.Hash` values, which do not fail. `DigestWriter` /
`VerifyingReader` return only those core sentinels (ADR 0014 §D4) — they mint no
codes of their own.

## Algorithms

| `Algorithm` | stdlib backing | digest | class |
|---|---|---|---|
| `sha256`   | `crypto/sha256.New`   | 32 B | cryptographic |
| `sha512`   | `crypto/sha512.New`   | 64 B | cryptographic |
| `sha3-256` | `crypto/sha3.New256`  | 32 B | cryptographic |
| `crc32c`   | `hash/crc32` (Castagnoli) | 4 B | **non**-cryptographic checksum |
| `fnv1a-64` | `hash/fnv.New64a`     | 8 B | **non**-cryptographic fingerprint |

`crc32c` / `fnv1a-64` are fast checksums for cache keys and in-memory dedup —
they are NOT collision-resistant; never use them where an adversary picks the
input.

## Conventions

- **Registration is a package-level `var`, never `init()`** (`KTN-FUNC-NOINIT`),
  mirroring the codec / AEAD convention.
- **Empty-struct schemes.** Each hasher is a `struct{}` so it is comparable —
  the registry's idempotent-vs-conflict check (`existing == h`) needs comparable
  values; a func-field adapter would break that.
- **Algorithm strings are frozen** post-v1.0.0 — a `SumHex` value stays
  reproducible across releases.

## Do NOT

- Add a keyed or password hash here — those belong to the (future) KDF /
  password ports, not the public-digest Hasher port.
- Treat `crc32c` / `fnv1a-64` as secure; document any new fast hash as
  non-cryptographic.
- Mint error codes here — dispatch errors and `DigestMismatch` live in
  `core/crypto`.
- Compare the `VerifyingReader` digest mid-stream or distinguish a tampered
  stream beyond the public, EOF-typed `DigestMismatch` — the check is
  deliberately terminal-only and non-oracle (ADR 0014 §D4).

## Verification

```sh
bazel test --config=race //internal/service/crypto/stdhash:stdhash_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./crypto/stdhash/...
```
