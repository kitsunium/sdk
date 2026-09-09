// Package token — the one-pass, string-aware JSON nesting counter.
package token

// depthScan is the state a linear nesting scan needs: how deep it currently
// is, and whether it is inside a JSON string (where a brace is data, not
// structure) and past a backslash (where the next byte is escaped).
//
// It is split out of checkJSONDepth so the "inside a string" rules live in one
// place: the escape handling is exactly where a hand-rolled scanner gets it
// wrong, and a `\"` read as a closing quote would make the rest of the payload
// look like structure.
type depthScan struct {
	// depth is the current bracket nesting level.
	depth int
	// inString reports whether the scan is inside a JSON string literal.
	inString bool
	// escaped reports whether the previous byte was an unconsumed backslash.
	escaped bool
}

// consumeStringByte advances the scan by one byte of string content.
func (s *depthScan) consumeStringByte(char byte) {
	//: a backslash protects exactly one byte, whatever it is.
	if s.escaped {
		s.escaped = false
		//: the protected byte is consumed; it can end nothing.
		return
	}
	//: a backslash starts an escape.
	if char == '\\' {
		s.escaped = true
		//: the next byte belongs to this escape.
		return
	}
	//: an unescaped quote ends the string.
	if char == '"' {
		s.inString = false
	}
}

// consumeStructuralByte advances the scan by one byte outside a string.
func (s *depthScan) consumeStructuralByte(char byte) {
	//: only four bytes carry structure; everything else is a scalar's body.
	switch char {
	//: a quote opens a string, where brackets stop counting.
	case '"':
		s.inString = true
	//: an object or array opens one level.
	case '{', '[':
		s.depth++
	//: and closes one. An unbalanced close only makes depth wrong-but-smaller;
	//: encoding/json is what rejects malformed structure.
	case '}', ']':
		s.depth--
	}
}
