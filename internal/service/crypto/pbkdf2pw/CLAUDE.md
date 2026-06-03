# internal/service/crypto/pbkdf2pw/

## Purpose

Registers the **"pbkdf2-sha256"** password-hashing scheme (ADR 0013).
Blank-importing the package — typically via `pkg/v1/password` — self-registers
the scheme so `crypto.HashPassword` / `crypto.VerifyPassword` resolve.
**Stdlib-only** (`crypto/pbkdf2` + `crypto/sha256` + `crypto/rand` +
`crypto/subtle`, all Go 1.26): zero non-stdlib deps, so it is the default that
keeps `pkg/v1/password` consumers dep-light.

PBKDF2-SHA256 at 600 000 iterations is the OWASP-recommended fallback where the
memory-hard argon2id is unavailable. argon2id (the OWASP first choice) lives
under `third-party/x-crypto` as an opt-in scheme — same precedent as
xchacha vs aesgcm.

## Contents

| File | Role |
|---|---|
| `pbkdf2pw.go` | `PasswordHasher` singleton, `pbkdf2PW` (`Algorithm` / `Hash` / `Verify` / `NeedsRehash`), `encodePHC` / `decodePHC` helpers |

No `codes.go` / `errors.go` — the scheme returns the shared `core/crypto`
sentinels (`PasswordHashFailed`, `InvalidPasswordHash`); it mints no codes.

## PHC string format

```
$pbkdf2-sha256$i=600000$<b64-salt>$<b64-digest>
```

- Un-padded base64 (`base64.RawStdEncoding`), the PHC convention.
- The id segment (`pbkdf2-sha256`) equals the `Algorithm`, so `VerifyPassword`
  resolves the scheme straight from the stored string — no side-channel.
- 128-bit random salt per hash, 256-bit digest.

## Behaviour

- **Hash** — fresh `crypto/rand` salt (→ `PasswordHashFailed` on an entropy
  fault), 600 000 iterations, PHC-encoded.
- **Verify** — re-derives with the stored salt + iteration count and compares
  with `subtle.ConstantTimeCompare`. Malformed PHC → `InvalidPasswordHash`;
  genuine mismatch → `(false, nil)` (non-oracle).
- **NeedsRehash** — true when the stored `i=` is below `currentIters` (600 000),
  driving transparent upgrade-on-verify.

## Do NOT

- Log the password or the digest; compare only via `subtle.ConstantTimeCompare`.
- Lower `currentIters` — it is a one-way ratchet (raising it makes old hashes
  `NeedsRehash`-stale, which is correct).
- Mint error codes here — they live in `core/crypto`.

## Verification

```sh
bazel test --config=race //internal/service/crypto/pbkdf2pw:pbkdf2pw_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./crypto/pbkdf2pw/...
```

## Accepted audit findings

- Deferred/accepted low+info audit findings (V71, V72) are recorded in `.claude/contexts/sdk-audit-2026-06-03-accepted.yaml` (2026-06-03 close-out). Each is a deliberate decision or deferred change, not an open bug.
