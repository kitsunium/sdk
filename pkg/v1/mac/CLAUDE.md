# pkg/v1/mac/

## Purpose

Public **detached message-authentication** facade (ADR 0014). `Tag` bytes with a
secret key, `Verify` the tag with the same key. Thin alias + delegation over
`internal/core/crypto`'s MAC registry — zero runtime cost, no business logic
(per `pkg/` convention).

Importing the package blank-imports `internal/service/crypto/hmacsha2`,
activating HMAC-SHA256 with **zero non-stdlib deps**.

## Surface

| Identifier | Role |
|---|---|
| `Algorithm` | alias of `corecrypto.Algorithm` (stable scheme id) |
| `Key` | alias of `corecrypto.Key` (redacting 256-bit key) |
| `HMACSHA256` | HMAC (RFC 2104) over SHA-256 |
| `NewKey(raw) (Key, error)` | 32-byte key; wrong length → `InvalidKey` |
| `Tag(a, key, message) ([]byte, error)` | tag; unknown algo → `UnknownMACAlgorithm` |
| `Verify(a, key, message, tag) (bool, error)` | `(true,nil)` valid · `(false,nil)` invalid · `(false, UnknownMACAlgorithm)` unregistered |

## A MAC tag is SECRET — compare with Verify, never ==

Unlike an unkeyed hash digest (a *public* fingerprint), a MAC tag is
secret-comparison-sensitive. Comparing it with `==` / `bytes.Equal` leaks timing.
Always route equality through `Verify`, which is constant-time. This is the exact
inverse of the `pkg/v1/hash` rule.

## vs the other crypto surfaces

- **mac** — detached integrity + authenticity under a *shared* secret.
- **sign** (`pkg/v1/sign`) — authenticity under a *public* key anyone verifies.
- **crypto** (`pkg/v1/crypto`) — confidentiality + integrity under a shared secret.
- **hash** (`pkg/v1/hash`) — unkeyed *public* fingerprints; no authentication.

## Conventions

- **Aliases, not new types** — `Algorithm`/`Key = corecrypto.*`; the const is the
  frozen wire string (`"hmac-sha256"`).
- **README.md is generated** (`make docs-readme` → gomarkdoc, ADR 0008). Edit the
  package doc comment in `mac.go`; never hand-edit `README.md`.
- **Signatures freeze at v1.0.0.** New scheme consts can be added; existing ones
  cannot move.

## Do NOT

- Compare tags with `==`/`bytes.Equal` — use `Verify`.
- Branch on `Verify`'s error to decide validity — validity is the bool; the error
  means "scheme not registered".
- Hand-author `README.md` — it is regenerated from the `mac.go` doc comment.

## Verification

```sh
bazel test --config=race //pkg/v1/mac:mac_test
# Fallback
cd pkg/v1 && GOWORK=off go test -race -cover ./mac/...
```
