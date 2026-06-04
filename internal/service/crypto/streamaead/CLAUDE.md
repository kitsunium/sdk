# internal/service/crypto/streamaead/

## Purpose

Registers the **"aes-256-gcm-stream"** streaming-AEAD scheme (ADR 0014 §D2).
Blank-importing the package — typically via `pkg/v1/crypto` — self-registers the
scheme so `crypto.SealStream` / `crypto.OpenStream` resolve. **Stdlib-only**
(`crypto/aes` + `crypto/cipher` + `crypto/hkdf` + `crypto/sha256` + `crypto/rand`):
it pulls zero non-stdlib deps, so it keeps `pkg/v1/crypto` consumers dep-light.
XChaCha streaming is a deferred follow-up (`third-party/x-crypto/*`); the merge
path is provably stdlib-only.

## FROZEN wire format (stream version `0x02`, disjoint from box `0x01`)

```
header : [1B stream-ver=0x02][1B algID=0x01][16B random salt]
key    : streamKey = HKDF-SHA256(ikm=key.Bytes(), salt=salt,
                                 info="kitsunium/stream-aead/v1")  -> 32B
chunks : fixed 64 KiB plaintext per chunk (final chunk 0..64 KiB)
         nonce_i (12B) = counter_i (11B big-endian) || flag (1B)
                         flag = 0x01 on the final chunk, else 0x00
         wire_i = AES-256-GCM-Seal(streamKey, nonce_i, plaintext_i, aad)
                = ct_i || tag(16)
```

This is the STREAM construction (Hoang–Reyhanitabar–Rogaway, as used by `age`):
the random per-stream salt makes the deterministic counter nonce safe even when
one `Key` encrypts many streams; the final-chunk **flag in the nonce** gives
truncation resistance. It is a **bespoke, SDK-owned frame** — NOT age/libsodium
interop. The header, the `info` string, and the KAT vectors are all SDK-owned and
frozen; a drift is a breaking change, never silent (the KAT recomputes the frame
independently from the stdlib).

- A writer ALWAYS emits a header + at least one final-flag chunk, even for an
  empty stream. A full 64 KiB data chunk is always followed by the (possibly
  empty) final chunk, so the reader's EOF look-ahead is unambiguous.
- **Hold-back:** the reader never surfaces a chunk's plaintext before its GCM tag
  verifies (GCM verifies the whole chunk before returning), and never before the
  chunk's `Open` succeeds.
- **Truncation** (EOF before a final-flag chunk) → `StreamTruncated`.
- **Counter overflow** (> 2^88 chunks, unreachable) is a hard error, never a wrap.
- **Lead-byte rejection:** the whole-buffer `Open` rejects a `0x02` stream (its
  version check fails); this reader rejects a `0x01` box / unknown version as
  `DecryptionFailed`.

## Contents

| File | Role |
|---|---|
| `streamaead.go` | `StreamSealer` singleton, `streamAEAD` (`Algorithm`/`Writer`/`Reader`), `newGCM` + `chunkNonce` helpers, frozen constants |
| `writer.go` | `streamWriter` (`io.WriteCloser`); chunk buffering + sealing; `newWriterWithSalt` (UNEXPORTED test-only salt seam) |
| `reader.go` | `streamReader` (`io.Reader`); header verify, chunk-by-chunk decrypt, one-byte EOF look-ahead, hold-back |

No `codes.go` / `errors.go` — the scheme returns the shared `core/crypto`
sentinels (`EntropyFailed` on a salt-draw fault, `StreamTruncated`,
`DecryptionFailed`, `UnknownAlgorithm` on a dispatch miss). It mints no codes of
its own, so it adds **no** `audit_srcs` to the root filegroup.

## Test-only seam

`newWriterWithSalt` is UNEXPORTED and exists so the KAT can pin a deterministic
salt; production `Writer` always draws a random salt from `crypto/rand`. No
consumer can pin a salt.

## Do NOT

- Change the header, `info` string, chunk size, nonce layout, or algID — the wire
  format is frozen; a change is a new stream version.
- Surface decrypted-but-unverified plaintext — the hold-back contract is load-bearing.
- Export the salt seam — deterministic salts in production reuse nonces.
- Log `key.Bytes()` / the derived stream key.

## Verification

```sh
bazel test --config=race //internal/service/crypto/streamaead:streamaead_test
make bench   # regenerates BENCH.md
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./crypto/streamaead/...
```

## Accepted audit findings

- Deferred/accepted low+info audit findings (V67, V68) are recorded in `.claude/contexts/sdk-audit-2026-06-03-accepted.yaml` (2026-06-03 close-out). Each is a deliberate decision or deferred change, not an open bug.
