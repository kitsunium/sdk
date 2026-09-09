// Package id wraps stdlib crypto/rand + the kernel clock as core/id.Generator
// implementations (UUIDv4, UUIDv7, ULID, snowflake, NanoID, KSUID, TypeID).
// Blank-importing this package self-registers every scheme that needs no
// configuration; snowflake, NanoID and TypeID also offer explicit constructors.
// No init() — registration is package-level var.
package id

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

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
	// crockfordChars is the number of base32 characters that cover a 128-bit
	// value. Shared by ULID and TypeID: both encode exactly 16 bytes this way,
	// they differ only in the case of the symbols.
	crockfordChars int = 26
	// bitsPerChar is the base32 group width (5 bits per Crockford char).
	bitsPerChar int = 5
	// crockfordPadBits is the zero pad at the MSB: 26*5 - 16*8 = 130-128 = 2.
	crockfordPadBits int = crockfordChars*bitsPerChar - uuidRawLen*bitsPerByte
	// crockfordFirstCharLimit is the EXCLUSIVE upper bound on the first
	// character's 5-bit value. crockfordPadBits of that group are pad bits the
	// encoder always writes as zero, so only bitsPerChar-crockfordPadBits real
	// bits are addressable there. A decoder that skips this check silently
	// accepts 130-bit strings and truncates them, which breaks the round trip.
	crockfordFirstCharLimit int = 1 << (bitsPerChar - crockfordPadBits)
	// dashedUUIDLen is the canonical dashed-hex UUID length (8-4-4-4-12).
	dashedUUIDLen int = 36
	// dashWidth is the width of one separator in the dashed form.
	dashWidth int = 1
	// uuidDash1 is the first separator offset (8). Each of the four is a hex
	// segment end shifted right by the dashes already emitted before it, so the
	// parser's grouping cannot drift from formatUUID's.
	uuidDash1 int = hexSeg1End
	// uuidDash2 is the second separator offset (13) — one dash precedes it.
	uuidDash2 int = hexSeg2End + dashWidth
	// uuidDash3 is the third separator offset (18) — two dashes precede it.
	uuidDash3 int = hexSeg3End + dashWidth + dashWidth
	// uuidDash4 is the fourth separator offset (23) — three dashes precede it.
	uuidDash4 int = hexSeg4End + dashWidth + dashWidth + dashWidth
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

// crockford32Encode renders the uuidRawLen-byte big-endian value b as
// crockfordChars base32 characters, MSB-first, with crockfordPadBits zero pad
// bits at the top. alphabet MUST hold 32 symbols indexed by their 5-bit value.
//
// ULID passes the uppercase Crockford set and TypeID the lowercase one: the bit
// packing is byte-for-byte identical, only the symbols differ, so keeping a
// single encoder is what stops the two schemes drifting apart.
func crockford32Encode(b []byte, alphabet string) string {
	//: one output byte per base32 character.
	var out [crockfordChars]byte
	//: walk each output character, most-significant group first.
	for i := range crockfordChars {
		//: accumulate bitsPerChar bits into v, MSB-first.
		v := 0
		//: pull each of the 5 bits for this character.
		for j := range bitsPerChar {
			//: position within the zero-padded 130-bit space, then de-pad.
			realIdx := i*bitsPerChar + j - crockfordPadBits
			//: pad bits (realIdx < 0) contribute a zero high bit.
			bit := 0
			//: real bits read MSB-first from the byte array.
			if realIdx >= 0 {
				bit = int((b[realIdx/bitsPerByte] >> (bitsPerByte - 1 - realIdx%bitsPerByte)) & 1)
			}
			//: shift the accumulator and OR in this bit.
			v = v<<1 | bit
		}
		//: map the 5-bit group to its alphabet character.
		out[i] = alphabet[v]
	}
	//: the assembled 26-char rendering.
	return string(out[:])
}

// crockford32Decode reverses crockford32Encode over the same alphabet,
// returning the uuidRawLen raw bytes s encodes. It rejects a wrong length, a
// symbol outside alphabet, and a first character whose pad bits are set —
// without that last check the decoder would silently drop the two high bits and
// stop being the inverse of the encoder.
func crockford32Decode(s, alphabet string) (raw [uuidRawLen]byte, err error) {
	//: the rendering is fixed-width; anything else is not this encoding.
	if len(s) != crockfordChars {
		//: name the rule that rejected it without echoing the input.
		return raw, errs.Wrap(Malformed, errs.WrapParams{},
			errs.String("rule", "length"), errs.Int("length", len(s)))
	}
	//: walk each character, most-significant group first.
	for i := range crockfordChars {
		//: resolve the symbol to its 5-bit value; -1 means it is not a symbol.
		v := strings.IndexByte(alphabet, s[i])
		//: a character outside the alphabet disqualifies the whole string.
		if v < 0 {
			//: name the rule and the offset, never the character itself.
			return raw, errs.Wrap(Malformed, errs.WrapParams{},
				errs.String("rule", "alphabet"), errs.Int("offset", i))
		}
		//: the leading group carries the pad bits, which the encoder zeroes.
		if i == 0 && v >= crockfordFirstCharLimit {
			//: a set pad bit means the string encodes more than 128 bits.
			return raw, errs.Wrap(Malformed, errs.WrapParams{},
				errs.String("rule", "overflow"))
		}
		//: scatter the 5 bits of this group back into the byte array.
		for j := range bitsPerChar {
			//: position within the zero-padded 130-bit space, then de-pad.
			realIdx := i*bitsPerChar + j - crockfordPadBits
			//: pad bits carry no payload — they were validated as zero above.
			if realIdx < 0 {
				//: nothing to write for a pad bit.
				continue
			}
			//: isolate bit j of the group, MSB-first.
			bit := byte(v>>(bitsPerChar-1-j)) & 1
			//: OR it into its position in the destination byte.
			raw[realIdx/bitsPerByte] |= bit << (bitsPerByte - 1 - realIdx%bitsPerByte)
		}
	}
	//: the fully reconstructed 128-bit value.
	return raw, nil
}

// parseUUID decodes the canonical dashed-hex form (8-4-4-4-12) back to its
// uuidRawLen raw bytes. It is the inverse of formatUUID and the entry point for
// re-labelling an EXISTING identifier, so it validates shape only — the version
// nibble is deliberately not enforced (see FormatTypeID).
func parseUUID(s string) (raw [uuidRawLen]byte, err error) {
	//: the canonical form is fixed-width.
	if len(s) != dashedUUIDLen {
		//: name the rule that rejected it without echoing the input.
		return raw, errs.Wrap(Malformed, errs.WrapParams{},
			errs.String("rule", "uuid_length"), errs.Int("length", len(s)))
	}
	//: the compacted hex digits, separators removed.
	var packed [hexFull]byte
	//: cursor into the compacted hex buffer.
	next := 0
	//: walk the dashed form once, copying hex digits and vetting separators.
	for i := range dashedUUIDLen {
		//: the four dash offsets follow from the 8-4-4-4-12 grouping.
		if isUUIDDashIndex(i) {
			//: a missing dash means the grouping is wrong.
			if s[i] != '-' {
				//: name the rule and the offset that failed.
				return raw, errs.Wrap(Malformed, errs.WrapParams{},
					errs.String("rule", "uuid_separator"), errs.Int("offset", i))
			}
			//: separators carry no payload.
			continue
		}
		//: everything else must be a hex digit; hex.Decode vets the alphabet.
		packed[next] = s[i]
		//: advance the compacted cursor.
		next++
	}
	//: decode the 32 compacted hex digits into the 16 raw bytes.
	if _, decErr := hex.Decode(raw[:], packed[:]); decErr != nil {
		//: a non-hex digit is the only failure hex.Decode can report here.
		return raw, errs.Wrap(decErr, errs.WrapParams{
			Code:    CodeIDMalformed,
			Reason:  "ID_MALFORMED",
			Public:  "The identifier is not well formed for its scheme",
			Private: "service/id: parseUUID found a non-hexadecimal digit in the dashed form",
		}, errs.String("rule", "uuid_alphabet"))
	}
	//: the fully reconstructed 128-bit value.
	return raw, nil
}

// isUUIDDashIndex reports whether i is one of the four separator offsets in the
// canonical dashed form.
func isUUIDDashIndex(i int) bool {
	//: the four offsets are derived from the hex segment ends (see the consts).
	return i == uuidDash1 || i == uuidDash2 || i == uuidDash3 || i == uuidDash4
}
