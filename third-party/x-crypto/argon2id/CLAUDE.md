# third-party/x-crypto/argon2id/

## Purpose

Registers the **"argon2id"** password-hashing scheme (ADR 0013) — the memory-hard,
OWASP-first-choice password hash. It depends on `golang.org/x/crypto/argon2`, so
it lives under `third-party/x-crypto` as an **opt-in** scheme (same precedent as
xchacha vs the stdlib aesgcm default): `pkg/v1/password` does NOT blank-import it,
keeping that facade dep-light. Consumers who can take the x/crypto dependency
blank-import this package, then `crypto.HashPassword("argon2id", …)` resolves and
`crypto.VerifyPassword` routes any stored hash back to its scheme by PHC id.

Lives in the **root** module (which hosts the x/crypto dep) and legitimately
imports `internal/core/crypto` + `internal/kernel/errs` (third-party packages may
reach into `internal/*` — they ship with the umbrella module, not `pkg/v1`).

## Contents

| File | Role |
|---|---|
| `argon2id.go` | `PasswordHasher` singleton, `argon2idPW` (`Algorithm`/`Hash`/`Verify`/`NeedsRehash`), `encodePHC`/`decodePHC`/`parseParams`/`parseUintField` helpers |

No `codes.go`/`errors.go` — it returns the shared `core/crypto` sentinels
(`PasswordHashFailed`, `InvalidPasswordHash`) and mints no codes.

## PHC string format

```
$argon2id$v=19$m=19456,t=2,p=1$<b64-salt>$<b64-digest>
```

- Standard argon2 PHC: version `v=19`, costs `m` (KiB), `t` (iterations), `p`
  (parallelism); un-padded base64 salt + digest.
- Current policy: `m=19456` (19 MiB), `t=2`, `p=1` (OWASP 2023), 128-bit salt,
  256-bit digest. `NeedsRehash` is true when the stored hash is weaker on ANY axis.

## Behaviour

- **Hash** — fresh `crypto/rand` salt (→ `PasswordHashFailed` on entropy fault),
  `argon2.IDKey` at the current policy, PHC-encoded.
- **Verify** — recompute with the stored salt + costs + output length, compare
  with `subtle.ConstantTimeCompare`. Malformed PHC → `InvalidPasswordHash`;
  mismatch → `(false, nil)` (non-oracle).
- **NeedsRehash** — true when stored `m`/`t`/`p` is below current policy.

## Do NOT

- Blank-import this from `pkg/v1/*` — it would pull `x/crypto` into the dep-light
  facade. It is opt-in by design.
- Log the password/digest; compare only via `subtle.ConstantTimeCompare`.
- Mint error codes here — they live in `core/crypto`.

## Verification

```sh
bazel test --config=race //third-party/x-crypto/argon2id:argon2id_test
```
