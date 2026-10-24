package jsonenc

import (
	"strconv"
	"unicode/utf8"
	"unsafe"

	"github.com/kitsunium/sdk/internal/kernel/swar"
)

// hexDigits spells the two digits of a \u00XX escape, lowercase as
// encoding/json writes them.
const hexDigits string = "0123456789abcdef"

// The bytes AppendEscaped never copies as they are, and the sizes of its loops.
const (
	// controlBound is the bound below which a byte is a control character, which
	// RFC 8259 §7 requires escaped. 0x20 is within swar's exact range (128).
	controlBound byte = 0x20
	// nibbleShift reaches a byte's high nibble.
	nibbleShift byte = 4
	// nibbleMask keeps a byte's low nibble.
	nibbleMask byte = 0x0f
	// byteValues is how many values a byte has: the size of the table of plain bytes.
	byteValues int = 256
	// scalarWindow is how many bytes the byte loop reads after the search finds
	// a byte: enough that a dense stretch pays for the search once, few enough
	// that a lone escape in a long string does not make the rest of it a byte loop.
	scalarWindow int = 16
	// dense is how many hits a window needs to be called dense, which keeps the
	// byte loop reading: the hit that ended the search is one of them.
	dense int = 2
	// searchMin is the length from which the word search beats the byte loop,
	// measured (BENCH.md): below it the fixed price of a search call is not repaid.
	searchMin int = 32
)

// The UTF-8 shapes the byte loop reads in place, without a decode call. C2 to
// DF lead a two-byte rune (C0 and C1 would be overlong); E1 to EC, EE and EF
// lead a three-byte rune that is neither overlong (E0) nor a surrogate (ED);
// a continuation byte is 10xxxxxx. E2 80 A8 and E2 80 A9 are the two
// separators, which the loop does not copy.
const (
	twoByteLead       byte = 0xC2
	twoByteMax        byte = 0xDF
	threeByteOverlong byte = 0xE0
	threeByteMax      byte = 0xEF
	threeByteSurr     byte = 0xED
	contMask          byte = 0xC0
	contBits          byte = 0x80
	twoBytes          int  = 2
	threeBytes        int  = 3
	separatorLead     byte = 0xE2
	separatorMid      byte = 0x80
	separatorLast     byte = 0xA8
)

// The two separators encoding/json escapes though RFC 8259 does not require it:
// legal in a JSON string, a line end in a script.
const (
	lineSeparator      rune = 0x2028
	paragraphSeparator rune = 0x2029
)

// replacement is U+FFFD as the three UTF-8 bytes it is written with.
const replacement string = "\xef\xbf\xbd"

var (
	// plain says, for each byte, that it is written as it is: printable ASCII
	// less the quote and the backslash (DEL included). Every other byte is a hit.
	plain = plainTable()
	// needsAttention is the one search every string takes: the next quote,
	// backslash, control byte or byte from 0x80 up. It is built once and read by
	// every call, so concurrent use is safe.
	needsAttention = swar.NewSet('"', '\\', controlBound, true)
)

// plainTable builds plain.
func plainTable() [byteValues]bool {
	var table [byteValues]bool
	//: printable ASCII, less the two bytes that need a backslash.
	for c := controlBound; c < utf8.RuneSelf; c++ {
		table[c] = c != '"' && c != '\\'
	}
	return table
}

// AppendString appends s to dst as a JSON string literal, quotes included,
// and returns the extended slice. See the package comment for the exact
// escapes: invalid UTF-8 becomes U+FFFD, U+2028 and U+2029 are escaped, and
// '<', '>' and '&' are not.
func AppendString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	dst = AppendEscaped(dst, s)
	//: the closing quote.
	return append(dst, '"')
}

// AppendEscaped appends the body of the JSON string literal of s — what
// [AppendString] writes between its quotes — so a caller can join several
// pieces into one literal, a group prefix and a key, before the closing
// quote.
func AppendEscaped(dst []byte, s string) []byte {
	//: a short string is not worth a search call: a byte loop answers it, and
	//: for the common string with nothing to escape that is one pass and one copy.
	if len(s) < searchMin {
		return appendShort(dst, s)
	}
	//: each pass copies the run before the next byte that needs attention.
	for len(s) >= searchMin {
		i := needsAttention.Index(readOnly(s))
		//: no such byte: the rest of the string is its own encoding.
		if i < 0 {
			//: the last run.
			return append(dst, s...)
		}
		dst = append(dst, s[:i]...)
		s = s[i:]
		//: the stretch after a hit is read by the byte loop, and for as long as it
		//: keeps finding bytes to escape: text full of escapes or of non-ASCII has
		//: one every few bytes, and a search call per hit would cost more than the
		//: loop it replaces. A window with a single hit is sparse text: back to
		//: the search.
		for hits := dense; hits >= dense && len(s) >= searchMin; {
			dst, s, hits = appendScalar(dst, s, scalarWindow)
		}
	}
	//: what is left is not worth a search call either.
	return appendShort(dst, s)
}

// appendShort is AppendEscaped for a string too short to repay the word search.
// A search call costs a copy of the set and a dispatch, a fixed price that a
// dozen bytes of text never earn back.
func appendShort(dst []byte, s string) []byte {
	//: the first byte that needs attention ends the copy of the plain prefix.
	for i := range len(s) {
		if !plain[s[i]] {
			dst = append(dst, s[:i]...)
			dst, _, _ = appendScalar(dst, s[i:], len(s))
			return dst
		}
	}
	//: nothing to escape.
	return append(dst, s...)
}

// appendScalar is the byte loop: it writes s up to the first byte at or past
// window that ends a run of plain bytes, and returns the rest and how many
// bytes it had to escape or replace (a valid multi-byte rune counts as one). It
// copies runs, breaks one for an escape, a bad byte or a separator, and passes
// a valid multi-byte rune through whole.
func appendScalar(dst []byte, s string, window int) (appended []byte, rest string, hits int) {
	//: start is the first byte of the run not yet copied, pos the one being read.
	start, pos := 0, 0
	//: stop once the window is read; a rune that began inside it is finished.
	for pos < len(s) && pos < window {
		c := s[pos]
		//: plain ASCII is copied as it is.
		if plain[c] {
			pos++
			continue
		}
		hits++
		//: an ASCII byte left is a quote, a backslash or a control byte.
		if c < utf8.RuneSelf {
			dst = appendEscapedByte(append(dst, s[start:pos]...), c)
			pos++
			start = pos
			continue
		}
		//: a valid two- or three-byte rune is read in place, no decode call (at
		//: most one of the two answers, by the lead byte).
		if size := twoByteSize(s[pos:]) + threeByteSize(s[pos:]); size > 0 {
			pos += size
			continue
		}
		dst, pos, start = appendDecoded(dst, s, pos, start)
	}
	//: the last run of the stretch.
	return append(dst, s[start:pos]...), s[pos:], hits
}

// isContinuation reports whether b is a UTF-8 continuation byte.
func isContinuation(b byte) bool {
	return b&contMask == contBits
}

// twoByteSize is 2 when s starts with a complete two-byte rune (U+0080 to
// U+07FF), else 0.
func twoByteSize(s string) int {
	if s[0] >= twoByteLead && s[0] <= twoByteMax && len(s) >= twoBytes && isContinuation(s[1]) {
		return twoBytes
	}
	return 0
}

// isSeparator reports whether s starts with E2 80 A8 or E2 80 A9, the two
// separators, which are escaped. It reads three bytes: the caller checked them.
func isSeparator(s string) bool {
	return s[0] == separatorLead && s[1] == separatorMid && s[2]&^1 == separatorLast
}

// isThreeByteLead reports whether b leads a three-byte rune that is neither
// overlong (E0) nor a surrogate (ED): E1 to EC, EE and EF.
func isThreeByteLead(b byte) bool {
	return b > threeByteOverlong && b <= threeByteMax && b != threeByteSurr
}

// threeByteSize is 3 when s starts with a complete three-byte rune that is not
// U+2028 or U+2029, else 0.
func threeByteSize(s string) int {
	if !isThreeByteLead(s[0]) || len(s) < threeBytes || !isContinuation(s[1]) || !isContinuation(s[2]) || isSeparator(s) {
		return 0
	}
	return threeBytes
}

// appendDecoded handles the rune at s[pos] that inPlaceRune did not take: a bad
// byte becomes U+FFFD, a separator its escape, any other valid rune stays in
// the run.
func appendDecoded(dst []byte, s string, pos, start int) (appended []byte, next, runStart int) {
	r, size := utf8.DecodeRuneInString(s[pos:])
	switch {
	//: a bad byte (size 1) is replaced; a real U+FFFD is three bytes and passes.
	case r == utf8.RuneError && size == 1:
		return append(append(dst, s[start:pos]...), replacement...), pos + 1, pos + 1
	//: the two separators are escaped.
	case r == lineSeparator || r == paragraphSeparator:
		dst = append(dst, s[start:pos]...)
		dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[r&rune(nibbleMask)])
		return dst, pos + size, pos + size
	//: any other valid rune stays in the run.
	default:
		return dst, pos + size, start
	}
}

// AppendInt appends v to dst in decimal, as a JSON number, and returns the
// extended slice.
func AppendInt(dst []byte, v int64) []byte {
	return strconv.AppendInt(dst, v, 10)
}

// AppendUint appends v to dst in decimal, as a JSON number, and returns the
// extended slice. A value past 2^53 is exact here and is not in a JavaScript
// reader; write it as a string when the reader is one.
func AppendUint(dst []byte, v uint64) []byte {
	return strconv.AppendUint(dst, v, 10)
}

// readOnly views s as a slice for the byte search, which only reads it. The
// caller guarantees len(s) > 0: the data pointer of an empty string is
// unspecified. No copy is made, which is the point: converting would allocate
// for any string past the compiler's small-buffer size.
func readOnly(s string) []byte {
	return unsafe.Slice(unsafe.StringData(s), len(s))
}

// appendEscapedByte appends the escape of one ASCII byte: a quote, a backslash
// or a control byte.
func appendEscapedByte(dst []byte, c byte) []byte {
	switch c {
	case '"', '\\':
		//: a backslash before the byte itself.
		return append(dst, '\\', c)
	case '\n':
		return append(dst, '\\', 'n')
	case '\r':
		return append(dst, '\\', 'r')
	case '\t':
		return append(dst, '\\', 't')
	case '\b':
		return append(dst, '\\', 'b')
	case '\f':
		return append(dst, '\\', 'f')
	default:
		//: every other control byte: \u00 and two hex digits.
		return append(dst, '\\', 'u', '0', '0', hexDigits[c>>nibbleShift], hexDigits[c&nibbleMask])
	}
}
