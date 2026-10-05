package toml

import "unicode/utf8"

// Digits an escape sequence carries.
const (
	// shortEscapeDigits is the length of \xHH (TOML v1.1.0).
	shortEscapeDigits int = 2
	// unicodeEscapeDigits is the length of \uXXXX.
	unicodeEscapeDigits int = 4
	// longEscapeDigits is the length of \UXXXXXXXX.
	longEscapeDigits int = 8
	// hexDigitBits is how many bits one hexadecimal digit carries.
	hexDigitBits uint = 4
	// hexLetterValue is the value of the hexadecimal digits a and A.
	hexLetterValue byte = 10
)

// Surrogates are not Unicode scalar values, so no escape may name one.
const (
	// surrogateFirst is the first UTF-16 surrogate.
	surrogateFirst uint32 = 0xD800
	// surrogateLast is the last UTF-16 surrogate.
	surrogateLast uint32 = 0xDFFF
)

// delimiterLength is the length of a multi-line string's delimiters.
const delimiterLength int = 3

// maxClosingQuotes is the longest run of quotes that can end a multi-line
// string: the three of the delimiter and two of the content.
const maxClosingQuotes int = 5

// stringValue reads a string value of any of the four spellings.
func (p *parser) stringValue(parent int32, part keyPart) error {
	at := p.pos
	n := leaf(kindString, part, at)
	quote := p.data[p.pos]
	var err error
	//: three quotes open a multi-line string.
	if p.hasTriple(quote) {
		n.text, err = p.multilineString(quote)
		n.flags |= flagTextEscaped
	} else {
		var escaped bool
		n.text, escaped, err = p.singleLineString()
		//: an escaped string lives in the unescape buffer.
		if escaped {
			n.flags |= flagTextEscaped
		}
	}
	//: a refusal inside the string.
	if err != nil {
		//: refused.
		return err
	}
	p.add(parent, n)
	//: the string.
	return nil
}

// hasTriple reports whether three quote characters start at p.pos.
func (p *parser) hasTriple(quote byte) bool {
	//: three of the same quote.
	return p.pos+2 < len(p.data) && p.data[p.pos+1] == quote && p.data[p.pos+2] == quote
}

// singleLineString reads a basic or literal string on one line, from its
// opening quote, and reports whether its value lives in the unescape buffer.
func (p *parser) singleLineString() (value span, escaped bool, err error) {
	//: a literal string has no escapes.
	if p.data[p.pos] == charApostrophe {
		value, err = p.literalString()
		//: always the document's own bytes.
		return value, false, err
	}
	//: a basic string.
	return p.basicString()
}

// literalString reads a literal string on one line: everything up to the next
// apostrophe, verbatim, except a newline or a control character other than
// the tab.
func (p *parser) literalString() (span, error) {
	p.pos++
	start := p.pos
	//: until the closing apostrophe.
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		//: the closing apostrophe.
		if c == charApostrophe {
			value := span{start: int32(start), end: int32(p.pos)}
			p.pos++
			//: the content, in place.
			return value, nil
		}
		//: printable ASCII and the tab are kept as they are.
		if byteClass[c]&classPrintable != 0 {
			p.pos++
			continue
		}
		//: a newline, a control character or invalid UTF-8 is refused.
		if err := p.skipRune(problemControlInString); err != nil {
			//: refused.
			return span{}, err
		}
	}
	//: the document ended inside the string.
	return span{}, p.fail(p.pos, problemUnterminatedString)
}

// basicString reads a basic string on one line. A string with no escape
// sequence is returned in place; the first backslash moves the value to the
// unescape buffer.
func (p *parser) basicString() (value span, escaped bool, err error) {
	p.pos++
	start := p.pos
	//: until the closing quote.
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		//: the closing quote.
		if c == charQuote {
			value = span{start: int32(start), end: int32(p.pos)}
			p.pos++
			//: the content, in place.
			return value, false, nil
		}
		//: an escape: from here on the value is rebuilt.
		if c == charBackslash {
			value, err = p.basicEscaped(start)
			//: the rebuilt value, or the refusal.
			return value, true, err
		}
		//: printable ASCII and the tab are kept as they are.
		if byteClass[c]&classPrintable != 0 {
			p.pos++
			continue
		}
		//: a newline, a control character or invalid UTF-8 is refused.
		if err := p.skipRune(problemControlInString); err != nil {
			//: refused.
			return span{}, false, err
		}
	}
	//: the document ended inside the string.
	return span{}, false, p.fail(p.pos, problemUnterminatedString)
}

// basicEscaped continues a basic string at its first backslash, rebuilding the
// value in the unescape buffer from start.
func (p *parser) basicEscaped(start int) (span, error) {
	out := int32(len(p.unescaped))
	p.unescaped = append(p.unescaped, p.data[start:p.pos]...)
	//: until the closing quote.
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		//: the closing quote.
		if c == charQuote {
			p.pos++
			//: the rebuilt value.
			return span{start: out, end: int32(len(p.unescaped))}, nil
		}
		//: one step: an escape, a printable byte or a character.
		if err := p.basicStep(c); err != nil {
			//: refused.
			return span{}, err
		}
	}
	//: the document ended inside the string.
	return span{}, p.fail(p.pos, problemUnterminatedString)
}

// basicStep copies the byte c at p.pos, or the escape sequence or character
// it starts, to the unescape buffer.
func (p *parser) basicStep(c byte) error {
	//: an escape sequence.
	if c == charBackslash {
		//: unescaped into the buffer.
		return p.escape()
	}
	//: printable ASCII and the tab.
	if byteClass[c]&classPrintable != 0 {
		p.unescaped = append(p.unescaped, c)
		p.pos++
		//: copied.
		return nil
	}
	start := p.pos
	//: a newline, a control character or invalid UTF-8 is refused.
	if err := p.skipRune(problemControlInString); err != nil {
		//: refused.
		return err
	}
	p.unescaped = append(p.unescaped, p.data[start:p.pos]...)
	//: one character, copied.
	return nil
}

// escape unescapes the sequence at p.pos, which is a backslash, into the
// unescape buffer.
func (p *parser) escape() error {
	at := p.pos
	p.pos++
	//: a backslash at the very end of the document.
	if p.pos >= len(p.data) {
		//: refused.
		return p.fail(at, problemBadEscape)
	}
	c := p.data[p.pos]
	//: the one-character escapes: \b \t \n \f \r \" \\, and \e of TOML v1.1.0.
	if b := simpleEscapes[c]; b != 0 {
		p.unescaped = append(p.unescaped, b)
		p.pos++
		//: one byte.
		return nil
	}
	p.pos++
	switch c {
	//: \uXXXX.
	case 'u':
		return p.hexEscape(at, unicodeEscapeDigits)
	//: \UXXXXXXXX.
	case 'U':
		return p.hexEscape(at, longEscapeDigits)
	//: \xHH, of TOML v1.1.0, which the previous library accepted.
	case 'x':
		return p.hexEscape(at, shortEscapeDigits)
	//: no other escape exists.
	default:
		return p.fail(at, problemBadEscape)
	}
}

// hexEscape reads the digits hexadecimal digits of an escape that started at
// offset at, and appends the character they name.
func (p *parser) hexEscape(at, digits int) error {
	//: the document ends before the digits do.
	if len(p.data)-p.pos < digits {
		//: refused.
		return p.fail(at, problemBadEscape)
	}
	// Unsigned and wide enough for eight digits: a rune would wrap \UFFFFFFFF
	// to a negative value that passes the range check below.
	var code uint32
	//: each digit, most significant first.
	for _, c := range p.data[p.pos : p.pos+digits] {
		//: only hexadecimal digits.
		if byteClass[c]&classHex == 0 {
			//: refused.
			return p.fail(at, problemBadEscape)
		}
		code = code<<hexDigitBits | uint32(hexValue(c))
	}
	//: a surrogate or a value past U+10FFFF is not a Unicode scalar value.
	if code > utf8.MaxRune || (code >= surrogateFirst && code <= surrogateLast) {
		//: refused.
		return p.fail(at, problemBadEscape)
	}
	p.pos += digits
	p.unescaped = utf8.AppendRune(p.unescaped, rune(code))
	//: the character, encoded.
	return nil
}

// multilineString reads a multi-line string from its opening delimiter into
// the unescape buffer. quote is the delimiter's character: a quote for a
// basic string, whose escapes are interpreted, an apostrophe for a literal
// one, kept verbatim.
func (p *parser) multilineString(quote byte) (span, error) {
	at := p.pos
	p.pos += delimiterLength
	//: a newline right after the opening delimiter is trimmed.
	p.newline()
	out := int32(len(p.unescaped))
	//: until the closing delimiter.
	for p.pos < len(p.data) {
		closed, err := p.multilineStep(quote)
		//: a refusal inside the string.
		if err != nil {
			//: refused.
			return span{}, err
		}
		//: the closing delimiter.
		if closed {
			//: the value.
			return span{start: out, end: int32(len(p.unescaped))}, nil
		}
	}
	//: the document ended inside the string.
	return span{}, p.fail(at, problemUnterminatedString)
}

// multilineStep copies one piece of a multi-line string to the unescape
// buffer, and reports whether it was the closing delimiter.
func (p *parser) multilineStep(quote byte) (closed bool, err error) {
	c := p.data[p.pos]
	//: a run of the delimiter's character: content, or the close.
	if c == quote {
		//: up to two quotes of content before the delimiter.
		return p.quoteRun(quote)
	}
	//: only a basic string interprets the backslash.
	if c == charBackslash && quote == charQuote {
		//: an escape, or a line-ending backslash.
		return false, p.multilineEscape()
	}
	//: printable ASCII and the tab, verbatim.
	if byteClass[c]&classPrintable != 0 {
		p.unescaped = append(p.unescaped, c)
		p.pos++
		//: copied.
		return false, nil
	}
	//: a newline, or a character.
	return false, p.multilineOther()
}

// multilineOther copies a newline, LF or CRLF, or a well-formed non-ASCII
// character, refusing a control character and a carriage return that does not
// end a line.
func (p *parser) multilineOther() error {
	start := p.pos
	//: newlines are content in a multi-line string, kept as written.
	if !p.newline() {
		//: anything but a character of two or more bytes is refused.
		if err := p.skipRune(problemControlInString); err != nil {
			//: refused.
			return err
		}
	}
	p.unescaped = append(p.unescaped, p.data[start:p.pos]...)
	//: copied.
	return nil
}

// quoteRun reads a run of the delimiter's character inside a multi-line
// string. Fewer than three are content; three to five close the string, the
// ones before the last three being content; more is refused (§String: "up to
// two quotes may appear next to the closing delimiter").
func (p *parser) quoteRun(quote byte) (closed bool, err error) {
	start := p.pos
	//: the length of the run.
	for p.pos < len(p.data) && p.data[p.pos] == quote {
		p.pos++
	}
	run := p.pos - start
	//: too many to place: three quotes may not appear inside the content.
	if run > maxClosingQuotes {
		//: refused.
		return false, p.fail(start, problemTooManyQuotes)
	}
	//: fewer than three are content; with three or more, the last three close.
	if run < delimiterLength {
		p.unescaped = append(p.unescaped, p.data[start:p.pos]...)
		//: content.
		return false, nil
	}
	p.unescaped = append(p.unescaped, p.data[start:p.pos-delimiterLength]...)
	//: the close.
	return true, nil
}

// multilineEscape handles a backslash in a multi-line basic string: a
// backslash that ends a line removes it and every whitespace and newline
// after it; any other is an escape sequence.
func (p *parser) multilineEscape() error {
	ahead := p.pos + 1
	//: whitespace between the backslash and the end of its line.
	for ahead < len(p.data) && (p.data[ahead] == charSpace || p.data[ahead] == charTab) {
		ahead++
	}
	saved := p.pos
	p.pos = ahead
	//: not the end of the line: an ordinary escape sequence.
	if !p.newline() {
		p.pos = saved
		//: \n, \u and the rest, or the refusal of an unknown one.
		return p.escape()
	}
	//: the newline is followed by any whitespace and newlines, all trimmed.
	for {
		p.skipSpace()
		//: no further newline: the next content starts here.
		if !p.newline() {
			//: trimmed.
			return nil
		}
	}
}

// hexValue returns the value of the hexadecimal digit c.
func hexValue(c byte) byte {
	switch {
	//: 0-9.
	case c <= '9':
		return c - '0'
	//: a-f.
	case c >= 'a':
		return c - 'a' + hexLetterValue
	//: A-F.
	default:
		return c - 'A' + hexLetterValue
	}
}
