# third-party/x-crypto/xchacha/

## Purpose

Registers the **"xchacha20poly1305"** AEAD scheme (ADR 0013). Blank-importing the
package self-registers it, so `crypto.SealAs(crypto.XChaCha20Poly1305, …)` and
`crypto.Open` resolve:

```go
import _ "github.com/kitsunium/sdk/third-party/x-crypto/xchacha"
box, _ := crypto.SealAs(crypto.XChaCha20Poly1305, k, plaintext, aad)
```

This is the **only** place `golang.org/x/crypto` enters a build for this scheme —
declared in the **root umbrella `go.mod`** under `third-party/x-crypto`, which no
other module requires. So `pkg/v1/crypto` consumers stay **dep-light** (zero
`x/crypto`) unless they opt in with this blank import — the same pattern as the
AWS writers under `third-party/aws/*` (ADR 0012).

## Why XChaCha20-Poly1305

A 192-bit (24-byte) random nonce eliminates the birthday-bound message-count
limit that AES-256-GCM's 96-bit random nonce imposes (~2^32 safe messages/key).
Preferred for high-volume random-nonce workloads. AES-256-GCM (`internal/service/
crypto/aesgcm`, `alg-id 0x01`) remains the dep-free default.

## Contents

| File | Role |
|---|---|
| `xchacha.go` | `AEAD` singleton, `xChaCha` (`Algorithm` / `ID` / `Seal` / `Open`), `newAEAD` helper |

No `codes.go` / `errors.go` — returns the shared `core/crypto` sentinels
(`InvalidKey`, `DecryptionFailed`) and wraps a `crypto/rand` fault as
`EntropyFailed`; mints no codes of its own.

## Wire format

```text
[1B Version=0x01][1B algID=0x02][24B nonce][ciphertext || 16B Poly1305 tag]
```

`Version` (`core/crypto.Version`) and `algID` (`0x02`) are frozen. The nonce is
generated from `crypto/rand` inside `Seal`, embedded in the clear, and never
reaches a caller. `Open` re-validates `Version` + `algID` + length, and returns
the single non-oracle `DecryptionFailed` for **every** failure.

## Do NOT

- Import this package from `pkg/v1/*` or the dep-light modules — it pulls
  `golang.org/x/crypto`. Activation is an explicit consumer blank import.
- Differentiate `Open` failures — that builds a padding/format oracle.
- Reuse or expose the nonce; it is internal to the box.

## Verification

```sh
# from the repo root (root umbrella module)
GOWORK=off go test -race -cover ./third-party/x-crypto/xchacha/...
```
