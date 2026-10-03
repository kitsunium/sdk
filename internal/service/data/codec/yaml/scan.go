// Package yaml — scalars: plain, single-quoted, double-quoted, and block.
package yaml

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// The reasons a plain scalar's segment on one line ends.
const (
	// stopEnd is the line break or the end of the document.
	stopEnd plainStop = iota
	// stopValue is ":" followed by a blank or the end of the line — in flow
	// context too: libyaml, and so yaml.v3, keeps a ":" followed by a flow
	// indicator inside the scalar, and the subset reads it as they do.
	stopValue
	// stopComment is a "#" after a blank.
	stopComment
	// stopFlow is a flow indicator, in flow context.
	stopFlow
	// stopQuestion is a "?" in flow context, where libyaml ends the scalar and
	// starts an explicit key; the subset refuses it rather than read it
	// another way.
	stopQuestion
)

// Block-scalar chomping, as its header's indicator says.
const (
	// chompStrip ("-") drops every trailing line break.
	chompStrip chomping = iota - 1
	// chompClip (no indicator) keeps exactly the last line break.
	chompClip
	// chompKeep ("+") keeps every trailing line break.
	chompKeep
)

// The characters YAML gives a double-quoted escape beyond C's, and the bounds
// of what a hexadecimal escape may name.
const (
	// runeNextLine is \N, U+0085.
	runeNextLine rune = 0x85
	// runeNoBreakSpace is \_, U+00A0.
	runeNoBreakSpace rune = 0xA0
	// runeLineSeparator is \L, U+2028.
	runeLineSeparator rune = 0x2028
	// runeParagraphSeparator is \P, U+2029.
	runeParagraphSeparator rune = 0x2029
	// runeEscape is \e, U+001B.
	runeEscape rune = 0x1B
	// surrogateFirst is the first UTF-16 surrogate, which no escape may name.
	surrogateFirst rune = 0xD800
	// surrogateLast is the last UTF-16 surrogate.
	surrogateLast rune = 0xDFFF
)

// The states of a block scalar being read.
const (
	// blockLiteral marks a literal (|) scalar rather than a folded one.
	blockLiteral blockFlags = 1 << iota
	// blockLeadingBreak says the last content line ended with a line break.
	blockLeadingBreak
	// blockLeadingBlank says the last content line started with a blank.
	blockLeadingBlank
)

// The widths of the hexadecimal escapes.
const (
	// hexByteDigits is the width of \xXX.
	hexByteDigits int = 2
	// hexBMPDigits is the width of \uXXXX.
	hexBMPDigits int = 4
	// hexRuneDigits is the width of \UXXXXXXXX.
	hexRuneDigits int = 8
)

// neverStartsPlain lists the indicators no plain scalar starts with, in any
// context.
const neverStartsPlain string = ",[]{}#&*!|>'\"%@`"

// The escapes of a double-quoted scalar.
var (
	// simpleEscapes maps the character after a backslash to the character it
	// names. "\/" is YAML 1.2's JSON escape, which libyaml — so yaml.v3 —
	// refuses; the subset reads nothing they refuse, so it is not here.
	simpleEscapes = map[byte]rune{
		'0': 0, 'a': '\a', 'b': '\b', 't': '\t', '\t': '\t', 'n': '\n',
		'v': '\v', 'f': '\f', 'r': '\r', 'e': runeEscape, ' ': ' ', '"': '"',
		'\\': '\\', 'N': runeNextLine, '_': runeNoBreakSpace,
		'L': runeLineSeparator, 'P': runeParagraphSeparator,
	}

	// hexEscapeWidths maps the character after a backslash to the number of
	// hexadecimal digits it takes.
	hexEscapeWidths = map[byte]int{'x': hexByteDigits, 'u': hexBMPDigits, 'U': hexRuneDigits}
)

// plainStop is why a plain scalar's segment on one line ended.
type plainStop uint8

// chomping is a block scalar's chomping indicator.
type chomping int

// quotedScan is the state of a quoted scalar being built.
type quotedScan struct {
	// line is the line the scalar opened on.
	line int
	// off is the offset of its opening quote.
	off int
	// blanks counts the raw blanks at the end of the text, which a fold drops.
	blanks int
	// quote is '"' or '\''.
	quote byte
	// singleLine refuses a line break: the scalar is a key.
	singleLine bool
}

// blockScalar is the state of a block scalar being read, as libyaml keeps it.
type blockScalar struct {
	// indent is the content's indentation.
	indent int
	// trailing counts the empty lines after the last content line.
	trailing int
	// flags holds the scalar's style and what the last content line was.
	flags blockFlags
}

// blockFlags are a block scalar's boolean states, as one value.
type blockFlags uint8

// blockHeader is a block scalar's header indicators.
type blockHeader struct {
	// chomp is the chomping indicator.
	chomp chomping
	// indicator is the indentation indicator, 0 when absent.
	indicator int
	// seenChomp says a chomping indicator was read.
	seenChomp bool
}

// isFlowIndicator reports whether c is one of the flow indicators , [ ] { }.
func isFlowIndicator(c byte) bool {
	//: one of the five.
	return c != 0 && strings.IndexByte(",[]{}", c) >= 0
}

// plainStart refuses a plain scalar's first character when YAML reserves it.
func (p *parser) plainStart(flow bool) error {
	//: an indicator.
	if p.startsIndicator(flow) {
		//: refused.
		return p.errHere("a plain scalar cannot start with an indicator character")
	}
	//: an ordinary start.
	return nil
}

// startsIndicator reports whether the character under the cursor opens an
// indicator rather than a plain scalar: one of neverStartsPlain, "-" followed
// by a blank, and — in block context — "?" or ":" followed by one. In flow
// context "?" and ":" always open an indicator.
func (p *parser) startsIndicator(flow bool) bool {
	c := p.peek()
	switch c {
	//: "-" before a blank, or before a flow indicator in flow context.
	case '-':
		//: an indicator.
		return p.blankOrEnd(p.pos+1) || (flow && isFlowIndicator(p.at(p.pos+1)))
	//: "?" and ":".
	case '?', ':':
		//: an indicator in flow context, or before a blank.
		return flow || p.blankOrEnd(p.pos+1)
	//: the rest.
	default:
		//: one of the reserved ones.
		return c != 0 && strings.IndexByte(neverStartsPlain, c) >= 0
	}
}

// plainSegment scans the plain scalar's text on the cursor's line, from the
// cursor, and returns the offset just past its last non-blank character and
// why it ended. The cursor does not move.
func (p *parser) plainSegment(flow bool) (int, plainStop) {
	end := p.pos
	//: one character at a time.
	for i := p.pos; i < len(p.src); i++ {
		c := p.src[i]
		//: a character that ends the segment.
		if stop, ok := p.plainStopAt(i, c, flow); ok {
			//: the segment so far.
			return end, stop
		}
		//: the last non-blank character so far.
		if !isBlank(c) {
			end = i + 1
		}
	}
	//: the document ends.
	return end, stopEnd
}

// plainStopAt reports whether the character c at i ends a plain scalar's
// segment, and why.
func (p *parser) plainStopAt(i int, c byte, flow bool) (plainStop, bool) {
	switch {
	//: the line ends.
	case c == '\n':
		//: the segment ends with it.
		return stopEnd, true
	//: the value indicator, before a blank.
	case c == ':':
		//: a key, when a blank follows.
		return stopValue, p.blankOrEnd(i + 1)
	//: a comment, after a blank.
	case c == '#':
		//: the scalar ends where the comment starts.
		return stopComment, i > p.pos && isBlank(p.src[i-1])
	//: nothing else ends a block scalar's segment.
	case !flow:
		//: content.
		return stopEnd, false
	//: an explicit key indicator, to libyaml.
	case c == '?':
		//: refused by the caller.
		return stopQuestion, true
	//: a flow indicator.
	default:
		//: the collection continues.
		return stopFlow, isFlowIndicator(c)
	}
}

// plainStopError refuses what ended a segment where it cannot end one: a "?"
// in flow context, a ":" value indicator in block context.
func (p *parser) plainStopError(end int, stop plainStop, flow bool) error {
	switch {
	//: a "?" inside a flow scalar.
	case stop == stopQuestion:
		//: refused where it is.
		return p.refuseQuestion(end)
	//: a ":" in a value: a mapping where none can start.
	case stop == stopValue && !flow:
		p.pos = end
		//: refused, as YAML does.
		return p.errHere("a mapping value is not allowed in this context")
	}
	//: an ordinary end.
	return nil
}

// refuseQuestion refuses the "?" that ended a plain scalar's segment at end
// in flow context: libyaml reads it as an explicit key indicator, the core
// grammar as text, and the subset reads it neither way.
func (p *parser) refuseQuestion(end int) error {
	p.pos = end
	//: the "?" itself, past the segment's trailing blanks.
	for p.peek() != '?' {
		p.pos++
	}
	//: refused where it is.
	return p.errHere("a plain scalar in a flow collection cannot hold '?': quote it")
}

// scanPlainKey scans a plain implicit key on the cursor's line, leaving the
// cursor on the ":" (or, in flow context, wherever the key ends).
func (p *parser) scanPlainKey(flow bool) (string, error) {
	//: the first character.
	if err := p.plainStart(flow); err != nil {
		//: refused.
		return "", err
	}
	start := p.pos
	end, stop := p.plainSegment(flow)
	//: a "?" inside a flow key.
	if stop == stopQuestion {
		//: refused where it is.
		return "", p.refuseQuestion(end)
	}
	//: in block context a key ends at its ":" on this line.
	if !flow && stop != stopValue {
		//: no ":" before the line ended or a comment started.
		return "", p.errHere("a mapping key must be followed by ':'")
	}
	p.pos = end
	//: the key's text.
	return p.src[start:end], nil
}

// scanPlain scans a plain scalar from the cursor, folding the lines it spans.
// The cursor ends just past the scalar's last non-blank character.
func (p *parser) scanPlain(parent int, flow bool) (string, error) {
	//: the first character.
	if err := p.plainStart(flow); err != nil {
		//: refused.
		return "", err
	}
	start := p.pos
	end, stop := p.plainSegment(flow)
	//: a "?" in flow, a ":" in block.
	if err := p.plainStopError(end, stop, flow); err != nil {
		//: refused.
		return "", err
	}
	p.pos = end
	//: a comment or an indicator ends it on its first line: the document's bytes.
	if stop != stopEnd {
		//: no copy.
		return p.src[start:end], nil
	}
	//: the line ends: later lines may continue it.
	return p.foldPlain(start, end, parent, flow)
}

// foldPlain continues the plain scalar src[start:end] onto the lines that
// continue it — indented deeper than parent, not a comment, not a document
// marker, not (in flow context) an indicator — folding the line breaks.
func (p *parser) foldPlain(start, end, parent int, flow bool) (string, error) {
	next, breaks, ok, err := p.plainContinuation(parent, flow)
	//: a single line, or a tab in the next line's indentation.
	if err != nil || !ok {
		//: the document's own bytes.
		return p.src[start:end], err
	}
	p.text = append(p.text[:0], p.src[start:end]...)
	//: one continuation line per iteration.
	for ok {
		p.text = appendFold(p.text, breaks)
		stop, serr := p.plainContinuationSegment(next, breaks, flow)
		//: a "?" in flow, a ":" in block.
		if serr != nil {
			//: refused.
			return "", serr
		}
		//: a comment or an indicator ends the scalar on this line.
		if stop != stopEnd {
			break
		}
		next, breaks, ok, err = p.plainContinuation(parent, flow)
		//: a tab in the next line's indentation.
		if err != nil {
			//: refused.
			return "", err
		}
	}
	//: the folded text.
	return string(p.text), nil
}

// plainContinuationSegment moves the cursor to the continuation line starting
// at next, breaks empty lines below the cursor's, and appends its segment.
func (p *parser) plainContinuationSegment(next, breaks int, flow bool) (plainStop, error) {
	p.line += breaks + 1
	p.pos, p.lineStart = next, next
	//: the continuation's first blanks are folded away.
	p.skipBlanks()
	segStart := p.pos
	segEnd, stop := p.plainSegment(flow)
	//: a "?" in flow, a ":" in block.
	if err := p.plainStopError(segEnd, stop, flow); err != nil {
		//: refused.
		return stop, err
	}
	p.text = append(p.text, p.src[segStart:segEnd]...)
	p.pos = segEnd
	//: why the segment ended.
	return stop, nil
}

// plainContinuation looks past the line break after the cursor for a line that
// continues a plain scalar. It returns that line's start, how many empty lines
// lie between, and whether there is one; the cursor does not move. A tab in
// an empty line's indentation — at or left of the parent's column — is
// refused, as libyaml refuses it while it scans the scalar.
func (p *parser) plainContinuation(parent int, flow bool) (next, breaks int, ok bool, err error) {
	i := p.pos
	//: blanks to the line break.
	for isBlank(p.at(i)) {
		i++
	}
	//: the scalar is on the document's last line.
	if p.at(i) != '\n' {
		//: no continuation.
		return 0, 0, false, nil
	}
	next = i + 1
	//: skip empty lines, counting them.
	for {
		j, terr := p.lineBlanks(next, parent, p.line+breaks+1)
		//: a tab in the indentation.
		if terr != nil {
			//: refused.
			return 0, 0, false, terr
		}
		//: a line with content, or the end.
		if p.at(j) != '\n' {
			//: whether it continues the scalar.
			return next, breaks, j < len(p.src) && p.continues(next, j, parent, flow), nil
		}
		breaks++
		next = j + 1
	}
}

// lineBlanks returns the offset of the first byte past the blanks opening the
// line at lineStart, refusing a tab at or left of the parent's column.
func (p *parser) lineBlanks(lineStart, parent, line int) (int, error) {
	j := lineStart
	//: the line's blanks.
	for isBlank(p.at(j)) {
		//: a tab where the indentation is.
		if p.at(j) == '\t' && j-lineStart <= parent {
			//: refused where it is.
			return 0, p.errAt(line, j, "a tab character cannot indent a line")
		}
		j++
	}
	//: past the blanks.
	return j, nil
}

// continues reports whether the content line starting at lineStart, whose
// first non-blank byte is at first, continues a plain scalar.
func (p *parser) continues(lineStart, first, parent int, flow bool) bool {
	indent := 0
	//: the spaces that indent it.
	for p.at(lineStart+indent) == ' ' {
		indent++
	}
	//: not deeper than the collection holding the scalar, a document marker,
	//: or a comment line — which ends a plain scalar.
	if indent <= parent || p.markerLineAt(lineStart) || p.at(first) == '#' {
		//: no.
		return false
	}
	//: in flow context, an indicator or a value indicator ends it.
	return !flow || !p.endsFlowScalar(first)
}

// markerLineAt reports whether the line starting at lineStart is a document
// marker.
func (p *parser) markerLineAt(lineStart int) bool {
	rest := p.src[lineStart:]
	//: either marker, then a separator.
	return (strings.HasPrefix(rest, "---") || strings.HasPrefix(rest, "...")) && p.blankOrEnd(lineStart+len("---"))
}

// endsFlowScalar reports whether the byte at i ends a plain scalar in flow
// context: a flow indicator, or ":" before a blank.
func (p *parser) endsFlowScalar(i int) bool {
	c := p.at(i)
	//: one or the other.
	return isFlowIndicator(c) || (c == ':' && p.blankOrEnd(i+1))
}

// appendFold appends the fold between two lines of a scalar: a space when no
// empty line separates them, otherwise one line feed per empty line.
func appendFold(text []byte, breaks int) []byte {
	//: adjacent lines.
	if breaks == 0 {
		//: a space.
		return append(text, ' ')
	}
	//: one line feed per empty line.
	for range breaks {
		text = append(text, '\n')
	}
	//: folded.
	return text
}

// scanSingleQuoted scans a single-quoted scalar from its opening quote: a
// doubled quote is a quote, every other character is itself, and line breaks
// fold. singleLine refuses a scalar that does not close on its line (a key).
func (p *parser) scanSingleQuoted(singleLine bool) (string, error) {
	//: the shared scanner.
	return p.scanQuoted('\'', singleLine)
}

// scanDoubleQuoted scans a double-quoted scalar from its opening quote,
// applying YAML's escapes and folding its line breaks. singleLine refuses a
// scalar that does not close on its line (a key).
func (p *parser) scanDoubleQuoted(singleLine bool) (string, error) {
	//: the shared scanner.
	return p.scanQuoted('"', singleLine)
}

// scanQuoted scans a quoted scalar whose opening quote is under the cursor.
// One that closes on its line with nothing to unescape is the document's own
// bytes; any other is built in p.text.
func (p *parser) scanQuoted(quote byte, singleLine bool) (string, error) {
	q := quotedScan{line: p.line, off: p.pos, quote: quote, singleLine: singleLine}
	p.pos++
	//: the common case, without a copy.
	if text, ok := p.quotedFast(quote); ok {
		//: the document's bytes.
		return text, nil
	}
	p.text = p.text[:0]
	//: one character, escape or fold per step, until the closing quote.
	for done := false; !done; {
		var err error
		done, err = p.quotedStep(&q)
		//: refused.
		if err != nil {
			return "", err
		}
	}
	//: the built text.
	return string(p.text), nil
}

// quotedFast returns the quoted scalar from the cursor when it closes on its
// line with no escape and no doubled quote, and moves past it.
func (p *parser) quotedFast(quote byte) (string, bool) {
	start := p.pos
	//: to the closing quote, or to what needs building.
	for i := start; i < len(p.src); i++ {
		//: the closing quote.
		if p.closesAt(i, quote) {
			p.pos = i + 1
			//: no copy.
			return p.src[start:i], true
		}
		//: an escape, a doubled quote or a line break.
		if needsBuilding(p.src[i], quote) {
			//: the slow path.
			return "", false
		}
	}
	//: not closed: the slow path reports it.
	return "", false
}

// closesAt reports whether the byte at i is a quote closing a scalar opened
// by quote: a single quote doubled is not one.
func (p *parser) closesAt(i int, quote byte) bool {
	//: the quote, not doubled when it is a single one.
	return p.src[i] == quote && (quote == '"' || p.at(i+1) != '\'')
}

// needsBuilding reports whether c, inside a quoted scalar opened by quote, is
// an escape, a quote that is not closing, or a line break.
func needsBuilding(c, quote byte) bool {
	//: a line break, a quote, a backslash in a double-quoted scalar.
	return c == '\n' || c == quote || (quote == '"' && c == '\\')
}

// quotedStep consumes one character, escape or fold of a quoted scalar and
// reports whether the scalar is closed.
func (p *parser) quotedStep(q *quotedScan) (bool, error) {
	c := p.peek()
	switch {
	//: the end of the document.
	case c == 0:
		//: refused where the scalar opened.
		return false, p.errAt(q.line, q.off, "a quoted scalar is not closed")
	//: a quote: closing, or doubled.
	case c == q.quote:
		//: closed, unless doubled.
		return p.quotedQuote(q), nil
	//: an escape.
	case c == '\\' && q.quote == '"':
		//: the escape.
		return false, p.quotedEscape(q)
	//: a line break folds.
	case c == '\n':
		//: the fold.
		return false, p.quotedBreak(q)
	}
	p.text = append(p.text, c)
	q.blanks++
	//: a non-blank character ends the run a fold would drop.
	if !isBlank(c) {
		q.blanks = 0
	}
	p.pos++
	//: open.
	return false, nil
}

// quotedQuote consumes the quote under the cursor and reports whether it
// closes the scalar; a doubled single quote is a quote.
func (p *parser) quotedQuote(q *quotedScan) bool {
	//: a doubled single quote.
	if q.quote == '\'' && p.at(p.pos+1) == '\'' {
		p.text = append(p.text, '\'')
		q.blanks = 0
		p.pos += 2
		//: still open.
		return false
	}
	p.pos++
	//: closed.
	return true
}

// quotedEscape consumes the escape under the cursor: a line break escaped
// joins the lines with nothing between, any other escape names a character.
func (p *parser) quotedEscape(q *quotedScan) error {
	q.blanks = 0
	//: an escape that names a character.
	if p.at(p.pos+1) != '\n' {
		//: the character.
		return p.escape()
	}
	//: a key fits on one line.
	if q.singleLine {
		//: refused.
		return p.errAt(q.line, q.off, "a mapping key must be written on one line")
	}
	p.pos++
	//: the next line's leading blanks go; empty lines stay.
	return p.joinEscapedBreak(q.line, q.off)
}

// quotedBreak folds the line break under the cursor.
func (p *parser) quotedBreak(q *quotedScan) error {
	//: a key fits on one line.
	if q.singleLine {
		//: refused.
		return p.errAt(q.line, q.off, "a mapping key must be written on one line")
	}
	p.text = p.text[:len(p.text)-q.blanks]
	q.blanks = 0
	//: fold to the next line with content.
	return p.foldQuoted(q.line, q.off)
}

// foldQuoted folds the line break under the cursor inside a quoted scalar
// that opened at (line, off): a space when the next line follows directly,
// one line feed per empty line otherwise, and the next line's leading blanks
// dropped. A document marker or the end of the document inside the scalar is
// refused.
func (p *parser) foldQuoted(line, off int) error {
	breaks := 0
	p.newline()
	p.skipBlanks()
	//: empty lines.
	for p.peek() == '\n' {
		breaks++
		p.newline()
		p.skipBlanks()
	}
	//: the end of the document, or a document marker.
	if err := p.insideQuoted(line, off); err != nil {
		//: refused.
		return err
	}
	p.text = appendFold(p.text, breaks)
	//: folded.
	return nil
}

// joinEscapedBreak handles a line break escaped by "\" in a double-quoted
// scalar: the break and the next line's leading blanks produce nothing, and
// each empty line in between produces a line feed.
func (p *parser) joinEscapedBreak(line, off int) error {
	p.newline()
	p.skipBlanks()
	//: empty lines after the escaped break are kept.
	for p.peek() == '\n' {
		p.text = append(p.text, '\n')
		p.newline()
		p.skipBlanks()
	}
	//: the end of the document, or a document marker.
	return p.insideQuoted(line, off)
}

// insideQuoted refuses a quoted scalar opened at (line, off) whose next line
// is the end of the document or a document marker.
func (p *parser) insideQuoted(line, off int) error {
	//: the document ends inside the scalar.
	if p.pos >= len(p.src) {
		//: refused where it opened.
		return p.errAt(line, off, "a quoted scalar is not closed")
	}
	//: a document marker cannot sit inside a scalar.
	if p.atDocumentMarker() {
		//: refused where it is.
		return p.errHere("a document marker inside a quoted scalar")
	}
	//: inside.
	return nil
}

// escape appends the character the escape under the cursor (a backslash)
// names, and moves past it.
func (p *parser) escape() error {
	c := p.at(p.pos + 1)
	//: a single-character escape.
	if r, ok := simpleEscapes[c]; ok {
		p.text = utf8.AppendRune(p.text, r)
		p.pos += 2
		//: escaped.
		return nil
	}
	//: a code point in hexadecimal.
	if width, ok := hexEscapeWidths[c]; ok {
		//: its digits.
		return p.hexEscape(width)
	}
	//: anything else.
	return p.errHere("an unknown escape sequence in a double-quoted scalar")
}

// hexEscape appends the code point a \x, \u or \U escape of width digits
// names.
func (p *parser) hexEscape(width int) error {
	start := p.pos + len(`\x`)
	//: the digits must all be there.
	if start+width > len(p.src) {
		//: refused where the escape is.
		return p.errHere("a hexadecimal escape is cut short")
	}
	value, err := strconv.ParseUint(p.src[start:start+width], 16, 32)
	//: not hexadecimal, past Unicode, or a surrogate — checked on the
	//: unsigned value: \U80000000 fits 32 bits and would wrap to a negative
	//: rune, which utf8.AppendRune writes as U+FFFD without a word.
	if err != nil || !isScalarValue(value) {
		//: refused where the escape is.
		return p.errHere("a hexadecimal escape does not name a Unicode character")
	}
	p.text = utf8.AppendRune(p.text, rune(value))
	p.pos = start + width
	//: escaped.
	return nil
}

// isScalarValue reports whether value is a Unicode scalar value: at most
// U+10FFFF, and not a UTF-16 surrogate.
func isScalarValue(value uint64) bool {
	//: within Unicode, outside the surrogates.
	return value <= utf8.MaxRune && (value < uint64(surrogateFirst) || value > uint64(surrogateLast))
}

// parseBlockScalar parses a literal (|) or folded (>) block scalar whose
// header is under the cursor. parent is the indentation of the collection
// holding it; its content is indented deeper. The algorithm is libyaml's, so
// a block scalar reads here as it reads in yaml.v3.
func (p *parser) parseBlockScalar(parent int) (int32, error) {
	line, off := p.line, p.pos
	b := blockScalar{}
	b.set(blockLiteral, p.peek() == '|')
	p.pos++
	header, err := p.readBlockHeader()
	//: a malformed header.
	if err != nil {
		//: refused.
		return noNode, err
	}
	p.text = p.text[:0]
	b.trailing, b.indent, err = p.blockBreaks(blockIndent(parent, header.indicator), parent, header.indicator == 0)
	//: a tab, or a misindented leading line.
	if err != nil {
		//: refused.
		return noNode, err
	}
	//: the content lines.
	if err := p.blockContent(&b); err != nil {
		//: refused.
		return noNode, err
	}
	p.chompBlock(&b, header.chomp)
	//: the scalar ended at a shallower line: the cursor goes back to its start.
	if p.pos < len(p.src) {
		p.pos = p.lineStart
	}
	//: the scalar.
	return p.scalar(string(p.text), styleBlock, line, off)
}

// blockIndent returns the content indentation an explicit indicator gives a
// block scalar inside parent: relative to the parent, and — as libyaml has
// it — absolute at the root. Without an indicator it is 0: detected.
func blockIndent(parent, indicator int) int {
	//: detected from the first content line.
	if indicator == 0 {
		//: unknown yet.
		return 0
	}
	//: at the root.
	if parent < 0 {
		//: absolute.
		return indicator
	}
	//: relative to the parent.
	return parent + indicator
}

// blockContent reads a block scalar's content lines into p.text, folding them
// when the scalar is folded.
func (p *parser) blockContent(b *blockScalar) error {
	//: one content line per iteration.
	for p.col() == b.indent && p.pos < len(p.src) {
		p.blockLineStart(b)
		lineStart := p.pos
		p.skipToEOL()
		p.text = append(p.text, p.src[lineStart:p.pos]...)
		//: the document ends on this line.
		if p.pos >= len(p.src) {
			b.trailing = 0
			//: done.
			return nil
		}
		p.newline()
		b.set(blockLeadingBreak, true)
		var err error
		b.trailing, _, err = p.blockBreaks(b.indent, 0, false)
		//: a tab where the content is indented.
		if err != nil {
			//: refused.
			return err
		}
	}
	//: the content ends.
	return nil
}

// blockLineStart writes what separates the previous content line from the
// one under the cursor: a folded space, or the line breaks kept as they are.
func (p *parser) blockLineStart(b *blockScalar) {
	trailingBlank := isBlank(p.peek())
	//: folding joins two plain lines.
	if b.folds(trailingBlank) {
		//: nothing between them: a space.
		if b.trailing == 0 {
			p.text = append(p.text, ' ')
		}
		b.set(blockLeadingBreak, false)
	}
	//: a break kept as is.
	if b.has(blockLeadingBreak) {
		p.text = append(p.text, '\n')
		b.set(blockLeadingBreak, false)
	}
	//: the empty lines before this one.
	for range b.trailing {
		p.text = append(p.text, '\n')
	}
	b.set(blockLeadingBlank, trailingBlank)
}

// has reports whether flag is set.
func (b *blockScalar) has(flag blockFlags) bool {
	//: the bit.
	return b.flags&flag != 0
}

// set sets or clears flag.
func (b *blockScalar) set(flag blockFlags, on bool) {
	//: set.
	if on {
		b.flags |= flag
		return
	}
	b.flags &^= flag
}

// folds reports whether the line break before a content line folds: in a
// folded scalar, between two lines neither of which starts with a blank.
func (b *blockScalar) folds(trailingBlank bool) bool {
	//: the folding rule.
	return !b.has(blockLiteral) && b.has(blockLeadingBreak) && !b.has(blockLeadingBlank) && !trailingBlank
}

// chompBlock applies the chomping indicator to the end of a block scalar.
func (p *parser) chompBlock(b *blockScalar, chomp chomping) {
	//: clip and keep keep the last line break.
	if chomp != chompStrip && b.has(blockLeadingBreak) {
		p.text = append(p.text, '\n')
	}
	//: keep also keeps the empty lines after it.
	if chomp == chompKeep {
		for range b.trailing {
			p.text = append(p.text, '\n')
		}
	}
}

// readBlockHeader reads a block scalar header after its indicator: a
// chomping indicator and an indentation indicator, in either order, then
// blanks, an optional comment and the line break.
func (p *parser) readBlockHeader() (blockHeader, error) {
	h := blockHeader{chomp: chompClip}
	//: at most one of each.
	for range 2 {
		//: one indicator.
		if err := p.headerIndicator(&h); err != nil {
			//: refused.
			return h, err
		}
	}
	//: a separator, a comment or the end must follow.
	if !p.blankOrEnd(p.pos) {
		//: refused.
		return h, p.errHere("a block scalar header holds an unknown indicator")
	}
	//: the rest of the header's line.
	return h, p.finishLine()
}

// headerIndicator reads one header indicator under the cursor, if there is
// one.
func (p *parser) headerIndicator(h *blockHeader) error {
	c := p.peek()
	switch {
	//: the chomping indicator.
	case (c == '+' || c == '-') && !h.seenChomp:
		h.seenChomp = true
		h.chomp = chompOf(c)
		p.pos++
	//: the indentation indicator.
	case c >= '1' && c <= '9' && h.indicator == 0:
		h.indicator = int(c - '0')
		p.pos++
	//: zero is not an indentation.
	case c == '0':
		//: refused.
		return p.errHere("a block scalar's indentation indicator cannot be 0")
	}
	//: read, or none.
	return nil
}

// chompOf returns the chomping a "+" or a "-" indicator says.
func chompOf(indicator byte) chomping {
	//: keep.
	if indicator == '+' {
		//: every trailing line break.
		return chompKeep
	}
	//: strip.
	return chompStrip
}

// blockBreaks consumes the empty lines of a block scalar from the start of a
// line, and the indentation of the first line with content, up to indent. It
// returns how many empty lines it consumed and, when detect is set, the
// indentation it detected from the first content line.
func (p *parser) blockBreaks(indent, parent int, detect bool) (breaks, detected int, err error) {
	maxEmpty := 0
	//: one line per iteration.
	for {
		//: the indentation, up to indent when it is known.
		if err := p.blockIndentation(indent); err != nil {
			//: refused.
			return 0, 0, err
		}
		//: a line with content, or the end.
		if p.peek() != '\n' {
			break
		}
		maxEmpty = max(maxEmpty, p.col())
		breaks++
		p.newline()
	}
	//: the indentation is known.
	if !detect {
		//: as given.
		return breaks, indent, nil
	}
	detected, err = p.detectIndent(parent, maxEmpty)
	//: the empty lines, and the indentation.
	return breaks, detected, err
}

// blockIndentation consumes a block scalar line's indentation: every space
// while the indentation is unknown, up to indent otherwise. A tab where an
// indentation space belongs is refused.
func (p *parser) blockIndentation(indent int) error {
	//: the spaces.
	for p.inBlockIndentation(indent) && p.peek() == ' ' {
		p.pos++
	}
	//: a tab where an indentation space belongs.
	if p.inBlockIndentation(indent) && p.peek() == '\t' {
		//: refused.
		return p.errHere("a tab character cannot indent a block scalar")
	}
	//: the indentation.
	return nil
}

// inBlockIndentation reports whether the cursor is still inside a block
// scalar line's indentation.
func (p *parser) inBlockIndentation(indent int) bool {
	//: unknown, or not reached.
	return indent == 0 || p.col() < indent
}

// detectIndent returns the indentation of the first content line, at least
// one deeper than the parent, refusing a leading empty line indented deeper
// than that line, which YAML forbids.
func (p *parser) detectIndent(parent, maxEmpty int) (int, error) {
	floor := max(parent+1, 1)
	//: a leading empty line deeper than the content.
	if p.pos < len(p.src) && p.col() >= floor && maxEmpty > p.col() {
		//: refused.
		return 0, p.errHere("a block scalar's leading empty line is indented deeper than its first line")
	}
	//: the first content line's indentation, or the floor.
	return max(p.col(), floor), nil
}
