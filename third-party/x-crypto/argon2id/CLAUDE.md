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
| `argon2id.go` | `PasswordHasher` singleton, `argon2idPW` (`Algorithm`/`Hash`/`Verify`/`NeedsRehash`), `encodePHC`/`decodePHC`/`decodeSaltDigest`/`parseParams`/`validCosts` helpers |

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

## Cost

**Every number here is large on purpose.** A password hash is slow and
memory-hungry so that an attacker's guess costs what an honest login costs. Full
calibration ladders — memory, iterations, parallelism — are in
`BENCH.md`; the shipped policy is:

| | ns/op | human | B/op |
|---|---:|---:|---:|
| `Hash` (`m=19456, t=2, p=1`) | 34 344 488 | **34.3 ms** | **19 926 768** |
| `Verify`, correct | 34 302 634 | 34.3 ms | 19 927 899 |
| `Verify`, wrong | 33 616 555 | 33.6 ms | 19 927 871 |
| `Verify`, malformed PHC | 114.3 | **114 ns** | 64 |
| `NeedsRehash` | 2 720 | 2.7 µs | 272 |
| PBKDF2-SHA256 `Hash` (the dep-free default) | 142 033 404 | 142.0 ms | 1 279 |

Four things an operator needs from that table:

- **argon2id is 4.1× FASTER than the SDK's default PBKDF2**, at each scheme's
  shipped policy. You do not buy argon2id with latency — you buy it with
  **15 600× the memory**, and the memory is the defence.
- **Peak RSS ≈ `concurrency × 19.9 MB`.** Eight concurrent logins is 159 MB; a
  hundred is **2.0 GB**. `-benchmem` reports it exactly (`B/op` = `m` × 1024 +
  ~2.2 KB), because argon2 allocates the whole block matrix in one `make`.
  Size the login path's concurrency limit, not just its CPU.
- **Portable tuning figures**, since the totals above are not portable:
  `cost(t) ≈ 1.9 ms + t × 15.7 ms` at `m = 19 MiB`, i.e. **0.884 ms per MiB per
  pass**. Memory is linear to ~32 MiB and **super-linear beyond**, which is the
  memory-hardness working.
- **`p` is a latency knob, not a cost knob.** `p=8` cuts a login from 33.3 ms to
  13.0 ms at identical memory and leaves an attacker's per-guess cost unchanged.
  Raising `p` without raising `m` or `t` weakens the policy.

`Verify` recomputes the digest in full and decides with
`subtle.ConstantTimeCompare`; the right/wrong gap measures 2.0 % and **flips sign
between runs**, which is what noise looks like. **That comparison is not to be
optimised or replaced.**

One measured observation, reported and deliberately not acted on:
`maxMem` (2 GiB) and `maxTime` (2²⁰) are **independent** caps, so they bound the
allocation a hostile stored PHC can request but not the *work* — their product
is the cost. A single `Verify` of a PHC declaring `m=2GiB, t=1` costs **≥2.2 s**
and 2 GiB; at `t=2²⁰` it is ≥25 days. See BENCH.md §"What the caps do not bound".

## Do NOT

- Blank-import this from `pkg/v1/*` — it would pull `x/crypto` into the dep-light
  facade. It is opt-in by design.
- Log the password/digest; compare only via `subtle.ConstantTimeCompare`.
- Mint error codes here — they live in `core/crypto`.

## Verification

```sh
bazel test --config=race //third-party/x-crypto/argon2id:argon2id_test
```

## Accepted audit findings

- Deferred/accepted low+info audit findings (V90, V91) are recorded in `.claude/contexts/sdk-audit-2026-06-03-accepted.yaml` (2026-06-03 close-out). Each is a deliberate decision or deferred change, not an open bug.
