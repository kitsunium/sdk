# encwrite

Logger middleware that encrypts each record's bytes before delivery to a
downstream sink (the "transform-the-bytes" category — ADR 0014 §D5).

## Layer

service (logger middleware). Wraps `core/logger.Sink`.

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
`nonce || ciphertext || tag` as produced by `crypto.Seal`.

## Error codes

Slot 0x1c. See `codes.go`.
