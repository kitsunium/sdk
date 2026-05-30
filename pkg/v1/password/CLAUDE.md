# pkg/v1/password/

## Purpose

Public **password-storage** facade (ADR 0013): slow, salted, self-describing
PHC-string hashes with transparent upgrade-on-verify. Thin alias + delegation
over `internal/core/crypto`'s PasswordHasher registry — zero runtime cost, no
business logic (per `pkg/` convention).

Importing the package blank-imports `internal/service/crypto/pbkdf2pw`,
activating PBKDF2-SHA256 with **zero non-stdlib deps**.

## Surface

| Identifier | Role |
|---|---|
| `Algorithm` | alias of `corecrypto.Algorithm` (stable scheme id == PHC id segment) |
| `PBKDF2SHA256` | PBKDF2-SHA256, the stdlib stretcher |
| `Hash(a, password) (string, error)` | PHC hash for NEW passwords; unknown algo → `UnknownPasswordAlgorithm` |
| `Verify(password, phc) (bool, error)` | constant-time check; scheme read from `phc`; mismatch → `(false, nil)`; malformed → error |
| `NeedsRehash(phc) bool` | true when `phc` is below its scheme's current cost policy |

## This is the ONLY surface for human passwords

Password hashing is deliberately **slow** and salted. Never run `hash.Sum` or
`kdf.Subkey` over a password — those are fast and assume high entropy. `Verify`
reads the scheme from the stored PHC string (no algorithm argument), compares in
constant time, and is **non-oracle** (a wrong password is `(false, nil)`; only a
malformed stored hash or unregistered scheme is an error).

### Upgrade-on-verify

```go
ok, _ := password.Verify([]byte(pw), stored)
if ok && password.NeedsRehash(stored) {
    stored, _ = password.Hash(password.PBKDF2SHA256, []byte(pw))  // re-store
}
```

## argon2id (opt-in, recommended in production)

argon2id is memory-hard (OWASP first choice) but pulls `golang.org/x/crypto`, so
it is NOT the dep-light default. Blank-import its `third-party/x-crypto` package
to register it, then `Hash` with its `Algorithm`; `Verify`/`NeedsRehash` already
work on any registered scheme's stored hashes (the PHC id routes them).

## Conventions

- **Aliases, not new types**; the const is the frozen PHC id (`"pbkdf2-sha256"`).
- **README.md is generated** (`make docs-readme` → gomarkdoc, ADR 0008). Edit the
  `password.go` doc comment; never hand-edit `README.md`.
- **Signatures freeze at v1.0.0.**

## Do NOT

- Compare `Hash` outputs with `==` or branch on `Verify`'s error to infer
  validity — validity is the bool; the error means malformed/unregistered.
- Feed a password to `hash`/`kdf`; use this facade.
- Hand-author `README.md`.

## Verification

```sh
bazel test --config=race //pkg/v1/password:password_test
# Fallback
cd pkg/v1 && GOWORK=off go test -race -cover ./password/...
```
