package toml

import "bytes"

// noPart is the key of a node that has none: an array element.
var noPart = keyPart{}

// value reads one value and adds it to parent under key part. parent is a
// table, with part its key, or an array, with noPart.
func (p *parser) value(parent int32, part keyPart) error {
	//: a key-value with nothing after the equals sign.
	if p.pos >= len(p.data) {
		//: refused.
		return p.fail(p.pos, problemExpectedValue)
	}
	//: the cap holds before the value is read, so a nested one never recurses past it.
	if p.nodes[parent].depth >= maxDepth {
		//: refused.
		return p.fail(p.pos, problemTooDeep)
	}
	c := p.data[p.pos]
	switch {
	//: a string, in any of its four spellings.
	case c == charQuote || c == charApostrophe:
		return p.stringValue(parent, part)
	//: an array.
	case c == charOpenBracket:
		return p.array(parent, part)
	//: an inline table.
	case c == charOpenBrace:
		return p.inlineTable(parent, part)
	//: true or false.
	case c == 't' || c == 'f':
		return p.boolValue(parent, part)
	//: a number, a date or a time.
	case byteClass[c]&classNumberStart != 0:
		return p.numberOrDate(parent, part)
	//: no value starts with anything else.
	default:
		return p.fail(p.pos, problemExpectedValue)
	}
}

// leaf returns a node of kind k keyed part, ready for the scalar it holds.
func leaf(k kind, part keyPart, at int) node {
	n := node{kind: k, key: part.name, at: int32(at)}
	//: the key's bytes live where the key parser put them.
	if part.escaped {
		n.flags |= flagKeyEscaped
	}
	//: the node, without its scalar.
	return n
}

// boolValue reads true or false.
func (p *parser) boolValue(parent int32, part keyPart) error {
	at := p.pos
	n := leaf(kindBool, part, at)
	switch {
	//: true is 1.
	case p.literal(wordTrue):
		n.num = 1
	//: false is 0.
	case p.literal(wordFalse):
	//: anything else starting with t or f is no value.
	default:
		return p.fail(at, problemExpectedValue)
	}
	n.text = span{start: int32(at), end: int32(p.pos)}
	p.add(parent, n)
	//: the boolean.
	return nil
}

// literal consumes word if the document continues with it.
func (p *parser) literal(word string) bool {
	//: the whole word must be there.
	if !bytes.HasPrefix(p.data[p.pos:], []byte(word)) {
		//: not this word.
		return false
	}
	p.pos += len(word)
	//: consumed.
	return true
}

// array reads an array value: values separated by commas, with whitespace,
// newlines and comments allowed around each, and an optional trailing comma
// (§Array).
func (p *parser) array(parent int32, part keyPart) error {
	arr := p.add(parent, leaf(kindArray, part, p.pos))
	p.pos++
	//: one element per round, until the closing bracket.
	for {
		//: whitespace, newlines and comments before an element.
		if err := p.skipBlank(); err != nil {
			//: a control character in a comment.
			return err
		}
		//: the array closes, empty or after a trailing comma.
		if p.consume(charCloseBracket) {
			//: done.
			return nil
		}
		//: the element.
		if err := p.value(arr, noPart); err != nil {
			//: refused.
			return err
		}
		//: what follows the element decides whether another one comes.
		if done, err := p.afterElement(); done || err != nil {
			//: closed, or refused.
			return err
		}
	}
}

// afterElement consumes what follows an array element: a comma, after which
// another element or the closing bracket comes, or the closing bracket. It
// reports whether the array closed.
func (p *parser) afterElement() (closed bool, err error) {
	//: whitespace, newlines and comments after the element.
	if err := p.skipBlank(); err != nil {
		//: a control character in a comment.
		return false, err
	}
	//: the array closes.
	if p.consume(charCloseBracket) {
		//: done.
		return true, nil
	}
	//: a comma before the next element.
	if p.consume(charComma) {
		//: another round.
		return false, nil
	}
	//: two values with no comma between them, or an unclosed array.
	return false, p.fail(p.pos, problemExpectedComma)
}

// skipBlank advances over whitespace, newlines and comments, where an array or
// an inline table allows them.
func (p *parser) skipBlank() error {
	//: until something that is none of the three.
	for {
		p.skipSpace()
		//: a comment runs to its newline.
		if p.pos < len(p.data) && p.data[p.pos] == charHash {
			//: checked as it is skipped.
			if err := p.comment(); err != nil {
				//: a control character in the comment.
				return err
			}
		}
		//: no newline: nothing more to skip.
		if !p.newline() {
			//: done.
			return nil
		}
	}
}

// inlineTable reads an inline table: key-values between braces, separated by
// commas (§Inline Table). The table is closed once read: nothing later in the
// document may add to it.
func (p *parser) inlineTable(parent int32, part keyPart) error {
	n := leaf(kindTable, part, p.pos)
	n.origin = originInline
	t := p.add(parent, n)
	p.pos++
	//: an empty inline table.
	if done, err := p.inlineClose(); done || err != nil {
		//: {} or a refusal.
		return err
	}
	//: one key-value per round.
	for {
		//: the key-value, its dotted keys relative to this table.
		if err := p.keyValue(t); err != nil {
			//: refused.
			return err
		}
		//: what follows the key-value.
		if done, err := p.afterPair(); done || err != nil {
			//: closed, or refused.
			return err
		}
	}
}

// afterPair consumes what follows a key-value of an inline table: the closing
// brace, or a comma and what follows it. It reports whether the table closed.
func (p *parser) afterPair() (closed bool, err error) {
	//: the table closes.
	if done, err := p.inlineClose(); done || err != nil {
		//: closed, or refused.
		return done, err
	}
	//: a comma before the next key-value.
	if !p.consume(charComma) {
		//: two key-values with no comma between them, or an unclosed table.
		return false, p.fail(p.pos, problemExpectedPairComma)
	}
	//: a trailing comma — TOML v1.1.0, which the previous library accepted.
	return p.inlineClose()
}

// inlineClose skips what may precede a closing brace and consumes the brace
// if it comes. Newlines and comments are allowed inside an inline table by
// TOML v1.1.0, not v1.0.0; they are accepted because the library this codec
// replaced accepted them, so no document that decoded before stops decoding.
func (p *parser) inlineClose() (closed bool, err error) {
	//: whitespace, newlines and comments.
	if err := p.skipBlank(); err != nil {
		//: a control character in a comment.
		return false, err
	}
	//: the closing brace, or not yet.
	return p.consume(charCloseBrace), nil
}
