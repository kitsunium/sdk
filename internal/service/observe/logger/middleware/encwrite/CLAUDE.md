<!-- updated: 2026-09-28T19:19:15Z -->
# encwrite

Logger middleware that encrypts each record's bytes before delivery to a
downstream sink (the "transform-the-bytes" category — ADR 0014 §D5).

## Layer

service (logger middleware). Wraps `core/observe/logger.Sink`.

## Why this shape

`EncWriter` derives a per-sink subkey from the configured master key via
`crypto.Subkey` (HKDF-SHA256) so the raw key never seals directly; this gives
each sink domain separation through the HKDF `info` label. Each record's bytes
are sealed with `crypto.Seal` (AES-256-GCM, nonce-prepended box) and then
length-prefixed with a 4-byte big-endian frame so byte sinks can recover
record boundaries. Writes are serialized under a mutex for concurrent-use
safety. `Close` zeroizes both the master key and the derived subkey on every
path before closing the downstream sink.

## Framing

`[4-byte big-endian uint32 = len(box)][sealed box]` where the sealed box is
the self-framed `[Version][algorithm id][nonce][ciphertext || tag]` produced by
`crypto.Seal`.

## Error codes

Slot 0x1c. See `codes.go`.

## Test lanes

| File | Tag | Lane that runs it |
|---|---|---|
| `encwrite_external_test.go`, `encwrite_internal_test.go` | — | `bazel test --config=race //...` (default) |
| `codes_internal_test.go` | `//go:build !race` | race-off alloc lane — `make test-alloc` (listed in `tools/alloc-lane-targets.txt`) — and CI's `test-386` job, where `Test_frame_overflow` skips: `len` cannot exceed `MaxUint32` on a 32-bit `int` |
| `encwrite_integration_test.go` | `//go:build integration` | **none** — opt-in, run by hand (below) |

`Test_EncWriter_Integration_RealSocket` drives a sealed-frame round trip over a
real `net` socket with the AES-GCM + HKDF-SHA256 schemes registered, so it is
excluded from the default build. Per rule 12 this is a *declared* exemption, not
an oversight — run it before shipping a change to the framing or the seal path:

```sh
cd internal/service && GOWORK=off go test -tags integration ./observe/logger/middleware/encwrite/...
```
