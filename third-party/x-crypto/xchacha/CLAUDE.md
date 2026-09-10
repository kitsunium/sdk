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

## Cost

Measured in `BENCH.md` against `internal/service/crypto/aesgcm` —
the dep-free default — through the same port, in the same run.

| plaintext | XChaCha `Seal` | AES-GCM `Seal` | verdict |
|---|---:|---:|---|
| 64 B | **760.1 ns** · 0.084 GB/s | 1 145 ns · 0.056 GB/s | XChaCha **1.51× faster** |
| 1 KiB | 1 847 ns · 0.554 GB/s | 1 913 ns · 0.535 GB/s | parity (within 4 %) |
| 64 KiB | 67 059 ns · 0.977 GB/s | **49 946 ns** · 1.312 GB/s | AES-GCM 1.34× faster † |
| 1 MiB | 1 430 257 ns · 0.733 GB/s | **707 543 ns** · 1.482 GB/s | AES-GCM **2.02× faster** |

† the 64 KiB rows carry a 15–21 % spread over nine samples and are the one place
not to quote three significant figures; `BENCH.md` says why.

**The crossover is at about 1 KiB**: at or below it the extended nonce is free —
the two schemes land within 4 % of each other in both directions across runs — and
at 1 MiB it costs 2.0× of AEAD throughput. `Open` is the same shape and is
cheaper than `Seal` at every size.

Two caveats that change what those rows mean:

- **The small-message win is a property of the PORT, not of the cipher.** With
  the cipher hoisted out of the loop, AES-GCM is 2.39× faster at 64 B too
  (213.4 ns against 510.7). XChaCha wins only because `Seal(Key, …)` forces
  AES-256 to rebuild its key schedule every call — 744.4 ns, 65 % of a 64-byte
  AES-GCM `Seal` — while `chacha20poly1305.NewX` costs 32.8 ns.
- **This is an AES-NI machine.** On a CPU without hardware AES the bulk columns
  are expected to invert; nothing here measures that.

`HChaCha20`, the subkey derivation that buys the 192-bit nonce, costs **≈176 ns
per call at any size** and runs in pure Go — `golang.org/x/crypto/chacha20`
ships no amd64 assembly for it.

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
