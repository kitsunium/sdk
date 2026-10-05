// Package streamaead (reader.go) — the streaming-AEAD read path: header verify,
// chunk-by-chunk decrypt with hold-back, and one-byte EOF look-ahead.
//
// Package streamaead registers the "aes-256-gcm-stream" streaming-AEAD scheme
// (ADR 0014 §D2). Importing the package (typically a blank import via
// pkg/v1/crypto) self-registers the scheme so crypto.SealStream /
// crypto.OpenStream resolve. It is stdlib-only (crypto/aes + crypto/cipher +
// crypto/hkdf + crypto/sha256 + crypto/rand), so it pulls zero non-stdlib deps
// and keeps pkg/v1/crypto consumers dep-light.
//
// # Frozen wire format (stream version 0x02, disjoint from the box's 0x01)
//
//	header : [1B stream-ver=0x02][1B algID=0x01][16B random salt]
//	key    : streamKey = HKDF-SHA256(ikm=key.Bytes(), salt=salt,
//	                                 info="kitsunium/stream-aead/v1") -> 32B
//	chunks : fixed 64 KiB plaintext per chunk (final chunk 0..64 KiB)
//	         nonce_i (12B) = counter_i (11B big-endian) || flag (1B)
//	                         flag = 0x01 on the final chunk, else 0x00
//	         wire_i = AES-256-GCM-Seal(streamKey, nonce_i, plaintext_i, aad)
//	                = ct_i || tag(16)
//
// This is the STREAM construction (Hoang-Reyhanitabar-Rogaway, as used by age):
// the random per-stream salt makes the deterministic counter nonce safe even
// when one Key encrypts many streams, and the final-chunk flag in the nonce
// provides truncation resistance — a truncated stream fails authentication
// because the Reader reaches EOF without ever decrypting a flag=0x01 chunk.
//
// This is a BESPOKE, SDK-owned frame — not age/libsodium interop. The header,
// the info string, and the KAT vectors are all SDK-owned and frozen.
//
// Package streamaead (writer.go) — the streaming-AEAD write path: lazy header
// emit, fixed-size chunk buffering, and per-chunk seal on overflow / Close.
package streamaead
