package json

import "strings"

// The bytes the sniffer accepts at the places it checks.
const (
	// jsonOutsideString is every byte JSON writes outside a string:
	// whitespace, punctuation, a number's bytes, and the letters of true,
	// false and null.
	jsonOutsideString string = " \t\n\r,:{}[]-+.eE0123456789truefalsn"
	// jsonValueStart is every byte a value, or an array's end, opens with.
	jsonValueStart string = `]"{[-0123456789tfn`
)

// Sniff implements codec.Sniffer (#909): prefix is JSON when, after leading
// whitespace, it opens an object or an array whose bytes so far are JSON's,
// and either ends inside that value or holds nothing but whitespace after it.
// A second value after the first is ndjson's, not this codec's; a scalar at
// the top level is too common a head to claim. It reads only prefix and
// allocates nothing.
func (*jsonCodec) Sniff(prefix []byte) bool {
	end, closed := scanJSONValue(prefix, skipJSONSpace(prefix, 0))
	//: not an object or an array, or a byte JSON never writes.
	if end < 0 {
		//: not this codec's.
		return false
	}
	//: cut inside the value, or alone in the input.
	return !closed || skipJSONSpace(prefix, end) == len(prefix)
}

// skipJSONSpace returns the index of the first byte at or after i that is not
// JSON whitespace.
func skipJSONSpace(p []byte, i int) int {
	//: space, tab, line feed, carriage return.
	for i < len(p) && (p[i] == ' ' || p[i] == '\t' || p[i] == '\n' || p[i] == '\r') {
		i++
	}
	//: the first other byte, or len(p).
	return i
}

// scanJSONValue scans the object or array opening at p[i] through its bytes:
// it returns the index just past it and closed when it ends within p, the
// length of p and !closed when p ends inside it, and -1 when p[i] opens no
// object or array, when an object's first member is not a string, or when a
// byte outside a string is one JSON never writes there.
func scanJSONValue(p []byte, i int) (end int, closed bool) {
	//: an object or an array, its first token plausible.
	if !opensJSONValue(p, i) {
		//: not one.
		return -1, false
	}
	depth := 0
	//: each byte, a string skipped whole.
	for j := i; j < len(p); j++ {
		//: a string: its bytes are its own.
		if p[j] == '"' {
			var ok bool
			j, ok = skipJSONString(p, j)
			//: a raw control byte JSON refuses in a string.
			if !ok {
				return -1, false
			}
			continue
		}
		depth += bracketDelta(p[j])
		//: the value closes.
		if depth == 0 {
			//: just past it.
			return j + 1, true
		}
		//: a byte JSON never writes outside a string.
		if strings.IndexByte(jsonOutsideString, p[j]) < 0 {
			return -1, false
		}
	}
	//: cut inside the value.
	return len(p), false
}

// opensJSONValue reports whether p[i] opens an object or an array whose first
// token can follow the bracket: a member name or the closing brace in an
// object, a value or the closing bracket in an array — or nothing, p ending
// there.
func opensJSONValue(p []byte, i int) bool {
	//: an object or an array.
	if i >= len(p) || (p[i] != '{' && p[i] != '[') {
		return false
	}
	k := skipJSONSpace(p, i+1)
	//: the prefix ends at the bracket: nothing contradicts it.
	if k == len(p) {
		return true
	}
	//: an object opens on a name or closes.
	if p[i] == '{' {
		return p[k] == '"' || p[k] == '}'
	}
	//: an array opens on any value or closes.
	return strings.IndexByte(jsonValueStart, p[k]) >= 0
}

// skipJSONString returns the index of the quote closing the string opening at
// p[i], or len(p) when p ends inside it; ok is false on a raw control byte.
func skipJSONString(p []byte, i int) (end int, ok bool) {
	//: each byte after the opening quote, an escape's next one skipped.
	for j := i + 1; j < len(p); j++ {
		switch c := p[j]; {
		case c == '"':
			return j, true
		case c == '\\':
			j++
		case c < ' ':
			return j, false
		}
	}
	//: cut inside the string.
	return len(p), true
}

// bracketDelta is +1 for a bracket that opens, -1 for one that closes, and 0
// for any other byte.
func bracketDelta(c byte) int {
	switch c {
	case '{', '[':
		return 1
	case '}', ']':
		return -1
	default:
		return 0
	}
}
