# internal/service/crypto/hmacsha2/

## Purpose

Registers the **"hmac-sha256"** MAC scheme (ADR 0014). Blank-importing the
package — typically via `pkg/v1/mac` — self-registers the scheme so
`crypto.MACTag` / `crypto.MACVerify` resolve. **Stdlib-only** (`crypto/hmac` +
`crypto/sha256`): it pulls zero non-stdlib deps, so it is the default detached-MAC
scheme that keeps `pkg/v1/mac` consumers dep-light.

HMAC-SHA256 (RFC 2104) is keyed, detached message authentication. Unlike an
unkeyed `Hasher` digest, a MAC tag **is** secret-comparison-sensitive: `Verify`
routes through `hmac.Equal` (constant-time), never `==`/`bytes.Equal` — the exact
inverse of the Hasher rule.

## Contents

| File | Role |
|---|---|
| `hmacsha2.go` | `MAC` singleton, `hmacSHA256` (`Algorithm` / `Tag` / `Verify` / `New`) |

No `codes.go` / `errors.go` — the scheme returns the shared `core/crypto`
sentinel `UnknownMACAlgorithm` (on a lookup miss in the dispatcher); it mints no
codes of its own, so it adds **no** `audit_srcs` to the root filegroup.

## Behaviour

- **Tag** — `hmac.New(sha256.New, key.Bytes())`, absorb the message, `Sum(nil)`.
  The redacting `Key` pins the length, so `Tag` cannot fail. Tag is 32 bytes.
- **Verify** — recompute the expected tag and compare with `hmac.Equal`
  (constant-time). Returns `false` for a tampered tag or a wrong key, never a
  timing oracle.
- **New** — a fresh keyed `hash.Hash` for incremental tagging over large inputs;
  the streaming and one-shot paths produce identical tags.

## Do NOT

- Compare tags with `==` or `bytes.Equal` — always route through `Verify`.
- Log `key.Bytes()`; it is the raw secret handed to the MAC.
- Mint error codes here — the dispatch (unknown-algorithm) error lives in
  `core/crypto`.

## Verification

```sh
bazel test --config=race //internal/service/crypto/hmacsha2:hmacsha2_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./crypto/hmacsha2/...
```
