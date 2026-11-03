package yaml

// yamlV3MaxDepth is gopkg.in/yaml.v3's own nesting bound: v3.0.1 refuses a
// document past 10 000 flow levels (max_flow_level) or 10 000 indentation
// levels (max_indents, scannerc.go), and says so only in its error text.
const yamlV3MaxDepth int = 10_000

// nestsPastV3Bound reports, for a document yaml.v3 has already refused,
// whether the refusal is its nesting bound: whether the bytes hold more than
// yamlV3MaxDepth flow collections open at once — '[' and '{' outside quoted
// scalars and comments — or a line opening more than yamlV3MaxDepth block
// sequences in a row ("- - - …"). It reads the structure, not the library's
// message, and it only ever names the reason of a refusal yaml.v3 made, so
// an over-count cannot refuse a document (#1099).
func nestsPastV3Bound(data []byte) bool {
	s := depthScan{data: data, lineStart: true}
	//: one byte, or one construct, at a time.
	for s.pos < len(data) {
		//: either bound crossed.
		if s.step() {
			//: the refusal was the nesting bound.
			return true
		}
	}
	//: the refusal was something else.
	return false
}

// depthScan is nestsPastV3Bound's cursor.
type depthScan struct {
	// data is the document.
	data []byte
	// pos is the next byte.
	pos int
	// flow counts the flow collections open.
	flow int
	// dashes counts the block sequences the current line opened.
	dashes int
	// lineStart is set until the line holds anything but blanks and "- ".
	lineStart bool
}

// step reads one byte or one construct and reports whether a bound was
// crossed.
func (s *depthScan) step() bool {
	c := s.data[s.pos]
	s.pos++
	//: what keeps the line's start, then what nests.
	if s.lineByte(c) {
		//: a run of "- " past the bound.
		return s.dashes > yamlV3MaxDepth
	}
	s.lineStart = false
	//: one class of byte per case.
	switch c {
	//: a quoted scalar: its brackets are text.
	case '"', '\'':
		s.pos = closingQuote(s.data, s.pos-1) + 1
	//: a comment runs to the end of its line.
	case '#':
		s.pos = lineEnd(s.data, s.pos-1) + 1
	//: a flow collection opens.
	case '[', '{':
		s.flow++
	//: one closes.
	case ']', '}':
		s.flow = max(s.flow-1, 0)
	}
	//: too many open at once.
	return s.flow > yamlV3MaxDepth
}

// lineByte handles the bytes that keep a line's start — a newline, blanks, a
// "- " entry while the line holds nothing else — and reports whether c was
// one of them.
func (s *depthScan) lineByte(c byte) bool {
	//: one class of byte per case.
	switch {
	//: a new line restarts the run of sequence entries.
	case c == '\n':
		s.lineStart, s.dashes = true, 0
	//: indentation.
	case c == ' ' || c == '\t':
	//: "- " opens one more block sequence on this line.
	case c == '-' && s.lineStart && s.pos < len(s.data) && s.data[s.pos] == ' ':
		s.dashes++
		s.pos++
	//: anything else.
	default:
		return false
	}
	//: handled.
	return true
}

// closingQuote returns the index of the quote closing the scalar opened at
// open, or the last index when it is never closed.
func closingQuote(data []byte, open int) int {
	//: one rule per quote style.
	if data[open] == '"' {
		//: backslash escapes.
		return closingDouble(data, open)
	}
	//: doubled quotes.
	return closingSingle(data, open)
}

// closingDouble finds the end of a double-quoted scalar, where a backslash
// escapes the next byte.
func closingDouble(data []byte, open int) int {
	//: scan to the closing quote.
	for i := open + 1; i < len(data); i++ {
		//: one rule per byte.
		switch data[i] {
		//: an escape.
		case '\\':
			i++
		//: the closing quote.
		case '"':
			return i
		}
	}
	//: unterminated.
	return len(data) - 1
}

// closingSingle finds the end of a single-quoted scalar, where a doubled
// quote stands for one.
func closingSingle(data []byte, open int) int {
	//: scan to the closing quote.
	for i := open + 1; i < len(data); i++ {
		//: not a quote.
		if data[i] != '\'' {
			continue
		}
		//: a doubled quote is one quote of the scalar.
		if i+1 < len(data) && data[i+1] == '\'' {
			i++
			continue
		}
		//: the closing quote.
		return i
	}
	//: unterminated.
	return len(data) - 1
}

// lineEnd returns the index of the newline ending the line at i, or the last
// index.
func lineEnd(data []byte, i int) int {
	//: to the newline, which the caller reads next.
	for i+1 < len(data) && data[i+1] != '\n' {
		i++
	}
	//: the last byte of the line.
	return i
}
