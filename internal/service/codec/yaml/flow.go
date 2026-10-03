// Package yaml — flow collections: [a, b] and {k: v}, across lines.
package yaml

// closingOf returns the indicator closing the flow collection opener opens.
func closingOf(opener byte) byte {
	//: a sequence.
	if opener == '[' {
		//: its bracket.
		return ']'
	}
	//: a mapping.
	return '}'
}

// flowCollection is the flow collection being parsed: where it opened, what
// closes it, and where its children start on the stack.
type flowCollection struct {
	// line is the line it opened on.
	line int
	// off is the offset of its opening indicator.
	off int
	// base is where its children start on the stack.
	base int
	// index is its node.
	index int32
	// kind is a sequence or a mapping.
	kind nodeKind
	// closing is "]" or "}".
	closing byte
}

// parseFlowCollection parses the flow sequence or mapping whose opening
// indicator is under the cursor. It may span lines; the cursor ends just past
// its closing indicator.
func (p *parser) parseFlowCollection(parent int) (int32, error) {
	f := flowCollection{line: p.line, off: p.pos, kind: kindSequence, closing: closingOf(p.peek())}
	//: a mapping.
	if p.peek() == '{' {
		f.kind = kindMapping
	}
	var err error
	f.index, f.base, err = p.open(f.kind)
	//: the depth or node bound.
	if err != nil {
		//: refused.
		return noNode, err
	}
	p.pos++
	//: one entry per iteration, until the closing indicator.
	for closed := false; !closed; {
		closed, err = p.flowStep(&f, parent)
		//: refused.
		if err != nil {
			return noNode, err
		}
	}
	p.close(f.index, f.base)
	//: a mapping's keys are unique.
	if f.kind == kindMapping {
		//: checked once it is whole.
		return f.index, p.checkDuplicates(f.index)
	}
	//: the sequence.
	return f.index, nil
}

// flowStep parses one entry of the flow collection f and the separator after
// it, and reports whether the collection is closed. A trailing comma before
// the closing indicator is allowed.
func (p *parser) flowStep(f *flowCollection, parent int) (bool, error) {
	//: blanks, line breaks, comments.
	if err := p.skipFlowSpace(f.line, f.off); err != nil {
		//: not closed.
		return false, err
	}
	//: closed, possibly after a trailing comma.
	if p.peek() == f.closing {
		p.pos++
		//: closed.
		return true, nil
	}
	//: an entry.
	if err := p.flowEntry(f.kind, parent); err != nil {
		//: refused.
		return false, err
	}
	//: blanks, line breaks, comments.
	if err := p.skipFlowSpace(f.line, f.off); err != nil {
		//: not closed.
		return false, err
	}
	switch p.peek() {
	//: another entry follows.
	case ',':
		p.pos++
		//: open.
		return false, nil
	//: closed.
	case f.closing:
		p.pos++
		//: closed.
		return true, nil
	//: anything else.
	default:
		//: refused where it is.
		return false, p.errHere("a flow collection entry must be followed by ',' or its closing indicator")
	}
}

// flowEntry parses one entry of a flow collection onto the stack: a node in
// a sequence (a single "key: value" pair becomes a one-entry mapping), a key
// and its value in a mapping.
func (p *parser) flowEntry(kind nodeKind, parent int) error {
	//: an empty entry: a separator with nothing before it.
	if p.peek() == ',' {
		//: refused.
		return p.errHere("a flow collection entry cannot be empty")
	}
	//: a sequence entry.
	if kind == kindSequence {
		entry, err := p.flowSequenceEntry(parent)
		//: refused.
		if err != nil {
			return err
		}
		p.stack = append(p.stack, entry)
		//: one node.
		return nil
	}
	key, value, err := p.flowPair(parent)
	//: refused.
	if err != nil {
		return err
	}
	p.stack = append(p.stack, key, value)
	//: two nodes.
	return nil
}

// flowSequenceEntry parses one node of a flow sequence. A scalar followed by
// ":" is a single pair, which YAML reads as a mapping of one entry.
func (p *parser) flowSequenceEntry(parent int) (int32, error) {
	line, off := p.line, p.pos
	//: a nested collection.
	if c := p.peek(); c == '[' || c == '{' {
		idx, err := p.parseFlowCollection(parent)
		//: refused.
		if err != nil {
			return noNode, err
		}
		p.skipBlanks()
		//: a collection followed by ":" is a key.
		if p.peek() == ':' {
			//: a complex key.
			return noNode, p.refuseAt(ComplexKeyRefused, line, off)
		}
		//: the collection.
		return idx, nil
	}
	value, style, err := p.flowScalar(parent)
	//: refused.
	if err != nil {
		return noNode, err
	}
	p.skipBlanks()
	//: not a pair: the scalar.
	if !p.atFlowValueIndicator(style) {
		//: the scalar node.
		return p.scalar(value, style, line, off)
	}
	//: an implicit key is written on one line.
	if p.line != line {
		//: refused where the key starts.
		return noNode, p.errAt(line, off, "a mapping key must be written on one line")
	}
	//: a single pair: a mapping of one entry.
	pair, base, err := p.open(kindMapping)
	//: the depth or node bound.
	if err != nil {
		return noNode, err
	}
	key, err := p.scalar(value, style, line, off)
	//: the node bound.
	if err != nil {
		return noNode, err
	}
	//: the ":" and the key's checks.
	if err := p.keyIndicator(line, off, style, value, true); err != nil {
		return noNode, err
	}
	val, err := p.flowValue(parent)
	//: refused.
	if err != nil {
		return noNode, err
	}
	p.stack = append(p.stack, key, val)
	p.close(pair, base)
	//: the one-entry mapping; its node sits where its key does.
	p.nodes[pair].off, p.nodes[pair].line = int32(off), int32(line)
	//: the pair.
	return pair, nil
}

// flowPair parses one entry of a flow mapping: a scalar key, then ":" and a
// value, or no ":" and the empty value ({a, b: 1} holds a: null).
func (p *parser) flowPair(parent int) (key, value int32, err error) {
	line, off := p.line, p.pos
	//: a collection as a key.
	if c := p.peek(); c == '[' || c == '{' {
		//: a complex key.
		return noNode, noNode, p.refuseAt(ComplexKeyRefused, line, off)
	}
	text, style, err := p.flowKey()
	//: refused.
	if err != nil {
		return noNode, noNode, err
	}
	key, err = p.scalar(text, style, line, off)
	//: the node bound.
	if err != nil {
		return noNode, noNode, err
	}
	end := p.pos
	p.skipBlanks()
	//: no ":": the key alone, with the empty value.
	if !p.atFlowValueIndicator(style) {
		//: a key alone is still bound, and still cannot be the merge key.
		if err := p.checkKey(line, off, end, style, text); err != nil {
			//: refused.
			return noNode, noNode, err
		}
		value, err = p.empty(p.line, p.pos)
		//: the pair.
		return key, value, err
	}
	//: the ":" and the key's checks.
	if err := p.keyIndicator(line, off, style, text, true); err != nil {
		return noNode, noNode, err
	}
	value, err = p.flowValue(parent)
	//: the pair.
	return key, value, err
}

// atFlowValueIndicator reports whether the cursor is on a ":" ending a key in
// flow context: followed by a blank, the end of the line or a flow indicator,
// or directly after a quoted key, as JSON writes it.
func (p *parser) atFlowValueIndicator(style scalarStyle) bool {
	//: not a ":".
	if p.peek() != ':' {
		//: no.
		return false
	}
	//: after a quoted key, any ":" is the indicator.
	if style != stylePlain {
		//: yes.
		return true
	}
	//: after a plain key, a blank or the line's end must follow — the plain
	//: scanner kept any other ":" inside the key, as libyaml does.
	return p.blankOrEnd(p.pos + 1)
}

// flowKey scans a flow mapping key: a quoted or plain scalar on one line.
func (p *parser) flowKey() (string, scalarStyle, error) {
	//: an anchor, an alias, a tag, a "?" — refused by name.
	if err := p.refuseNodeIndicator(true); err != nil {
		//: refused.
		return "", stylePlain, err
	}
	switch p.peek() {
	//: double-quoted.
	case '"':
		value, err := p.scanDoubleQuoted(true)
		//: the key.
		return value, styleDouble, err
	//: single-quoted.
	case '\'':
		value, err := p.scanSingleQuoted(true)
		//: the key.
		return value, styleSingle, err
	//: plain.
	default:
		//: a ":" opens a value with no key.
		if p.peek() == ':' {
			//: refused.
			return "", stylePlain, p.errHere("a mapping key cannot be empty")
		}
		value, err := p.scanPlainKey(true)
		//: the key.
		return value, stylePlain, err
	}
}

// flowValue parses the value after a ":" in flow context: a node, or the
// empty node when the entry ends there.
func (p *parser) flowValue(parent int) (int32, error) {
	line, off := p.line, p.pos
	//: the value may sit on a later line.
	if err := p.skipFlowSpace(line, off); err != nil {
		//: not closed.
		return noNode, err
	}
	//: the entry ends: an empty value.
	if c := p.peek(); c == ',' || c == ']' || c == '}' {
		//: the empty node.
		return p.empty(line, off)
	}
	//: a nested collection.
	if c := p.peek(); c == '[' || c == '{' {
		//: whatever it holds.
		return p.parseFlowCollection(parent)
	}
	valueLine, valueOff := p.line, p.pos
	value, style, err := p.flowScalar(parent)
	//: refused.
	if err != nil {
		return noNode, err
	}
	//: the scalar node.
	return p.scalar(value, style, valueLine, valueOff)
}

// flowScalar scans a scalar in flow context: quoted, or plain across lines.
func (p *parser) flowScalar(parent int) (string, scalarStyle, error) {
	//: an anchor, an alias, a tag, a "?" — refused by name.
	if err := p.refuseNodeIndicator(true); err != nil {
		//: refused.
		return "", stylePlain, err
	}
	switch p.peek() {
	//: double-quoted.
	case '"':
		value, err := p.scanDoubleQuoted(false)
		//: the scalar.
		return value, styleDouble, err
	//: single-quoted.
	case '\'':
		value, err := p.scanSingleQuoted(false)
		//: the scalar.
		return value, styleSingle, err
	//: plain.
	default:
		value, err := p.scanPlain(parent, true)
		//: the scalar.
		return value, stylePlain, err
	}
}

// skipFlowSpace moves the cursor past blanks, line breaks and comments inside
// a flow collection that opened at (line, off). The end of the document, or a
// document marker, inside the collection is refused.
func (p *parser) skipFlowSpace(line, off int) error {
	//: until a token.
	for {
		moved, err := p.flowSpaceStep()
		//: a token, or a document marker.
		if !moved || err != nil {
			break
		}
	}
	//: the end of the document inside the collection.
	if p.pos >= len(p.src) {
		//: refused where the collection opened.
		return p.errAt(line, off, "a flow collection is not closed")
	}
	//: a token, or a refusal.
	return p.flowSpaceError()
}

// flowSpaceStep moves the cursor past one blank, line break or comment, and
// reports whether it moved.
func (p *parser) flowSpaceStep() (bool, error) {
	switch c := p.peek(); {
	//: a blank.
	case isBlank(c):
		p.pos++
	//: a line break.
	case c == '\n':
		p.newline()
	//: a comment, which a blank or a line start separates.
	case c == '#' && (p.pos == p.lineStart || isBlank(p.src[p.pos-1])):
		p.skipToEOL()
	//: a token, or the end.
	default:
		//: not moved.
		return false, nil
	}
	//: moved; a document marker at a new line stops the skip.
	return !p.atDocumentMarker(), nil
}

// flowSpaceError refuses a document marker the skip stopped on: a document
// cannot end inside a flow collection.
func (p *parser) flowSpaceError() error {
	//: a document marker cannot sit inside a collection.
	if p.atDocumentMarker() {
		//: refused where it is.
		return p.errHere("a document marker inside a flow collection")
	}
	//: a token.
	return nil
}
