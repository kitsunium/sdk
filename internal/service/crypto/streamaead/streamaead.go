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
package streamaead

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"io"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// algorithm is the canonical registry key for the streaming AES-256-GCM scheme.
	algorithm corecrypto.Algorithm = "aes-256-gcm-stream"
	// streamVersion is the frozen leading byte of every stream header. It is
	// disjoint from the whole-buffer box Version (0x01) so Open/OpenStream can
	// reject the other format's lead byte.
	streamVersion byte = 0x02
	// algID is the frozen 1-byte algorithm id for this scheme in the header.
	algID byte = 0x01
	// versionFieldLen is the [version][algID] prefix width before the salt.
	versionFieldLen int = 2
	// saltLen is the random per-stream HKDF salt length in bytes.
	saltLen int = 16
	// headerLen is the fixed header length: version + algID + salt.
	headerLen int = versionFieldLen + saltLen
	// chunkSize is the fixed plaintext chunk size (64 KiB); the final chunk may
	// be shorter (0..chunkSize). It is part of the frozen format.
	chunkSize int = 64 * 1024
	// flagLen is the 1-byte flag suffix of the nonce (final vs non-final).
	flagLen int = 1
	// nonceLen is GCM's 96-bit nonce: a counterLen counter || a flagLen flag.
	nonceLen int = 12
	// counterLen is the big-endian chunk counter width inside the nonce.
	counterLen int = nonceLen - flagLen
	// counterUint64Bytes is the width of the uint64 written into the counter field.
	counterUint64Bytes int = 8
	// counterBE64Offset positions the counterUint64Bytes-wide big-endian counter
	// in the low bytes of the counter field, leaving the high bytes zero.
	counterBE64Offset int = counterLen - counterUint64Bytes
	// gcmTagLen is the GCM authentication tag appended to each chunk's ciphertext.
	gcmTagLen int = 16
	// flagFinal marks the final chunk's nonce; truncation resistance rides on it.
	flagFinal byte = 0x01
	// flagMore marks every non-final chunk's nonce.
	flagMore byte = 0x00
	// hkdfInfo is the frozen, SDK-owned HKDF info string binding the derived
	// stream key to this construction.
	hkdfInfo string = "kitsunium/stream-aead/v1"
)

// StreamSealer is the registered streaming AES-256-GCM scheme singleton (no
// init(); package-level var initialiser, mirroring the codec/AEAD convention).
var StreamSealer = corecrypto.RegisterStreamSealer(streamAEAD{})

// streamAEAD implements core/crypto.StreamSealer over stdlib AES-256-GCM with an
// HKDF-derived per-stream key.
type streamAEAD struct{}

// Algorithm reports the canonical algorithm key.
func (streamAEAD) Algorithm() corecrypto.Algorithm {
	//: the literal key consumers pass to SealStream/OpenStream.
	return algorithm
}

// Writer wraps dst so writes are sealed under key with aad. It draws a fresh
// random salt, derives the per-stream key, and constructs the writer with that
// salt; Close writes the final authenticated chunk. A host entropy fault
// surfaces as EntropyFailed.
func (streamAEAD) Writer(key corecrypto.Key, dst io.Writer, aad []byte) (sealed io.WriteCloser, err error) {
	//: a fresh random per-stream salt makes the counter nonce safe across streams.
	var salt [saltLen]byte
	//: fill it from crypto/rand; a fault here is a host entropy problem.
	if _, rerr := rand.Read(salt[:]); rerr != nil {
		//: wrap the cause as the typed EntropyFailed sentinel, no key bytes.
		return nil, errs.Wrap(rerr, errs.WrapParams{
			Code:    corecrypto.CodeEntropyFailed,
			Reason:  "ENTROPY_FAILED",
			Public:  "Could not gather entropy for encryption",
			Private: "service/crypto/streamaead.Writer: crypto/rand.Read failed while generating a salt",
		})
	}
	//: build the writer with the freshly drawn salt (test-only seam reuses this).
	return newWriterWithSalt(key, dst, aad, salt)
}

// Reader wraps src so reads are opened under key with aad. It reads + verifies
// the header, derives the per-stream key, and enforces the hold-back contract: a
// chunk's plaintext is never surfaced before its GCM tag verifies, and a stream
// cut short before its final-flag chunk surfaces as StreamTruncated.
func (streamAEAD) Reader(key corecrypto.Key, src io.Reader, aad []byte) (opened io.Reader, err error) {
	//: construct the lazy reader; the header is read on the first Read call so a
	//: missing import or wrong-format lead byte surfaces at use, not construction.
	return newReader(key, src, aad), nil
}

// newGCM builds the GCM cipher.AEAD from the HKDF-derived 32-byte stream key. A
// derivation or cipher fault surfaces as DecryptionFailed on the read path and
// is impossible on the write path (the salt + key are always well-formed).
func newGCM(keyBytes, salt []byte) (mode cipher.AEAD, err error) {
	//: derive the per-stream key: HKDF-SHA256 over the master key + random salt.
	streamKey, kerr := hkdf.Key(sha256.New, keyBytes, salt, hkdfInfo, corecrypto.KeyLen)
	//: HKDF only fails on an over-long length (unreachable: 32 < ceiling).
	if kerr != nil {
		//: collapse to the non-oracle data error.
		return nil, corecrypto.DecryptionFailed
	}
	//: 32-byte derived key selects AES-256.
	block, berr := aes.NewCipher(streamKey)
	//: a wrong-size key is rejected by the cipher constructor (unreachable).
	if berr != nil {
		//: same non-oracle data error.
		return nil, corecrypto.DecryptionFailed
	}
	//: standard 96-bit-nonce GCM over the AES block.
	gcm, gerr := cipher.NewGCM(block)
	//: NewGCM only fails on an exotic block size (defensive).
	if gerr != nil {
		//: same non-oracle data error for the unreachable path.
		return nil, corecrypto.DecryptionFailed
	}
	//: ready-to-use authenticated cipher keyed by the derived stream key.
	return gcm, nil
}

// chunkNonce builds the 12-byte nonce for chunk counter with the given flag: a
// counterLen-byte big-endian counter (the high counterLen-counterUint64Bytes
// bytes stay zero) followed by the 1-byte flag. Counter overflow is structurally
// impossible: a uint64 counter occupies only the low counterUint64Bytes of the
// counterLen-byte field, so it can never wrap into the flag or reuse a nonce —
// the ADR's "overflow is a hard error, never a silent wrap" holds by construction.
func chunkNonce(counter uint64, flag byte) [nonceLen]byte {
	//: a zero-valued nonce buffer to fill with the counter + flag.
	var nonce [nonceLen]byte
	//: write the counter big-endian into the low bytes of the counter field; the
	//: high counter bytes stay zero (a uint64 never reaches them).
	binary.BigEndian.PutUint64(nonce[counterBE64Offset:counterLen], counter)
	//: the final byte of the nonce is the chunk flag.
	nonce[nonceLen-1] = flag
	//: the fully-built nonce for this chunk.
	return nonce
}
