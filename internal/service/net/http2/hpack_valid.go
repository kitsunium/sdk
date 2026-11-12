package http2

import (
	"github.com/kitsunium/sdk/internal/kernel/swar"
)

// The bytes RFC 9113 §8.2.1 looks at.
const (
	// asciiTab and asciiSpace are the whitespace a value may not begin or end
	// with.
	asciiTab   byte = '\t'
	asciiSpace byte = ' '
	// asciiColon opens a pseudo-field's name and nothing else.
	asciiColon byte = ':'
	// asciiLF and asciiCR are, with NUL, never in a value.
	asciiLF byte = '\n'
	asciiCR byte = '\r'
	// firstUpper and lastUpper bound the capitals a field name may not hold.
	firstUpper byte = 'A'
	lastUpper  byte = 'Z'
	// firstVisible is the lowest byte a field name may hold.
	firstVisible byte = 0x21
	// lastVisible is the highest byte a field name may hold.
	lastVisible byte = 0x7e
)

// valueStop is what a field value may not hold: NUL, LF and CR, RFC 9113
// §8.2.1.
var valueStop = swar.NewSet(asciiLF, asciiCR, 1, false)

// validField reports whether f's name and value are well-formed, RFC 9113
// §8.2.1.
func validField(f *decodedField) bool {
	return validName(f.name) && validValue(f.value)
}

// validName reports whether name is a well-formed field name: lowercase,
// visible ASCII, the first colon of a pseudo-field only.
func validName(name []byte) bool {
	//: an empty name is malformed.
	if len(name) == 0 {
		return false
	}
	rest := name
	//: a pseudo-field's name opens with its single colon.
	if rest[0] == asciiColon {
		rest = rest[1:]
	}
	//: every other byte: visible ASCII, no capital, no colon.
	for _, c := range rest {
		//: the first bad byte settles it.
		if !nameByte(c) {
			return false
		}
	}
	return true
}

// nameByte reports whether c may be in a field name after its first colon.
func nameByte(c byte) bool {
	return c >= firstVisible && c <= lastVisible && (c < firstUpper || c > lastUpper) && c != asciiColon
}

// validValue reports whether value is a well-formed field value: no NUL, LF
// or CR, no leading or trailing whitespace.
func validValue(value []byte) bool {
	//: the empty value is allowed.
	if len(value) == 0 {
		return true
	}
	return !isOWS(value[0]) && !isOWS(value[len(value)-1]) && valueStop.Index(value) < 0
}

// isOWS reports whether c is a space or a tab.
func isOWS(c byte) bool {
	return c == asciiSpace || c == asciiTab
}
