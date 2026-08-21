// Package id wraps stdlib crypto/rand + the kernel clock as core/id.Generator
// implementations (UUIDv4, UUIDv7, ULID, snowflake). Blank-importing this
// package self-registers every stateless scheme; snowflake also offers a
// stateful per-node constructor. No init() — registration is package-level var.
package id

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// uuidRawLen is the 128-bit UUID byte length.
	uuidRawLen int = 16
	// hexSeg1End is the end of the first hex segment (4 bytes → 8 hex chars).
	hexSeg1End int = 8
	// hexSeg2End is the end of the second hex segment.
	hexSeg2End int = 12
	// hexSeg3End is the end of the third hex segment.
	hexSeg3End int = 16
	// hexSeg4End is the end of the fourth hex segment.
	hexSeg4End int = 20
	// hexFull is the full hex length of a 16-byte UUID (32 chars).
	hexFull int = 32
	// uuidVersionIdx is the byte carrying the version nibble (RFC 9562).
	uuidVersionIdx int = 6
	// uuidVariantIdx is the byte carrying the variant bits (RFC 9562).
	uuidVariantIdx int = 8
	// uuidVersionMask clears the high nibble of the version byte.
	uuidVersionMask byte = 0x0F
	// uuidVariantMask clears the two high bits of the variant byte.
	uuidVariantMask byte = 0x3F
	// uuidVariantBits sets the RFC 4122/9562 variant (10xx_xxxx).
	uuidVariantBits byte = 0x80
	// uuidVersion4 is the version-4 nibble pre-shifted into the high nibble.
	uuidVersion4 byte = 0x40
	// uuidVersion7 is the version-7 nibble pre-shifted into the high nibble.
	uuidVersion7 byte = 0x70
	// tsBytes is the 48-bit millisecond-timestamp width shared by UUIDv7 + ULID.
	tsBytes int = 6
	// bitsPerByte is the obvious 8, named to satisfy the no-magic-number rule.
	bitsPerByte int = 8
)

// readRandom fills b with cryptographically-secure random bytes, wrapping any
// CSPRNG fault in the EntropyFailed sentinel.
func readRandom(b []byte) error {
	//: crypto/rand.Read fills b fully or returns an error (never short).
	_, err := rand.Read(b)
	//: success fast-path.
	if err == nil {
		//: b now holds len(b) secure random bytes.
		return nil
	}
	//: wrap the CSPRNG fault with the dotted-quad entropy code.
	return errs.Wrap(err, errs.WrapParams{
		Code:    CodeIDEntropyFailed,
		Reason:  "ID_ENTROPY_FAILED",
		Public:  "Identifier generation failed to read secure random bytes",
		Private: "service/id: crypto/rand.Read returned an error",
	})
}

// formatUUID renders the 16-byte raw form b as the canonical 36-char dashed hex
// (8-4-4-4-12). b MUST be uuidRawLen bytes.
func formatUUID(b []byte) string {
	//: hex-encode the full 16 bytes to 32 chars, then splice the four dashes.
	hexStr := hex.EncodeToString(b)
	//: build the dashed form from the five hex segments.
	return hexStr[0:hexSeg1End] + "-" + hexStr[hexSeg1End:hexSeg2End] + "-" +
		hexStr[hexSeg2End:hexSeg3End] + "-" + hexStr[hexSeg3End:hexSeg4End] + "-" +
		hexStr[hexSeg4End:hexFull]
}

// setUUIDBits stamps the version nibble (high nibble of byte 6) and the RFC
// variant bits (high bits of byte 8) into the raw form. versionHighNibble is
// the 4-bit UUID version pre-shifted into the high nibble by the caller.
func setUUIDBits(b []byte, versionHighNibble byte) {
	//: keep the low nibble of byte 6, set the version in the high nibble.
	b[uuidVersionIdx] = (b[uuidVersionIdx] & uuidVersionMask) | versionHighNibble
	//: keep the low 6 bits of byte 8, set the 10xx variant in the high 2 bits.
	b[uuidVariantIdx] = (b[uuidVariantIdx] & uuidVariantMask) | uuidVariantBits
}

// putUint48BE writes the low 48 bits of v into dst[0:tsBytes] big-endian. dst
// MUST have at least tsBytes bytes. Shared by UUIDv7 and ULID.
func putUint48BE(dst []byte, v int64) {
	//: emit the most-significant byte first across the 6-byte window.
	for i := range tsBytes {
		//: shift so byte 0 carries bits 47..40, byte 5 carries bits 7..0.
		shift := (tsBytes - 1 - i) * bitsPerByte
		//: mask to a single byte after the shift.
		dst[i] = byte(v >> shift)
	}
}
