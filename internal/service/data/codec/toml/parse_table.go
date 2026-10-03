// Package toml — table headers, key-value pairs and keys: the half of the
// parser that decides where a value goes and whether TOML allows it there.
package toml

// header reads a [table] or [[array of tables]] header and makes its table
// the current one.
func (p *parser) header() error {
	at := p.pos
	array := p.pos+1 < len(p.data) && p.data[p.pos+1] == charOpenBracket
	p.pos++
	//: [[ opens an array of tables; the two brackets are one token.
	if array {
		p.pos++
	}
	p.skipSpace()
	//: the key names the table.
	if err := p.key(); err != nil {
		//: not a key.
		return err
	}
	//: the closing bracket, or both.
	if !p.consume(charCloseBracket) || (array && !p.consume(charCloseBracket)) {
		//: refused where the header should have closed.
		return p.fail(p.pos, problemUnclosedHeader)
	}
	parent, err := p.walkHeader(p.parts[:len(p.parts)-1])
	//: an intermediate key that is not a table.
	if err != nil {
		//: refused.
		return err
	}
	last := p.parts[len(p.parts)-1]
	//: the two headers part ways only at the last key.
	if array {
		//: append an element.
		return p.openArrayTable(parent, last, at)
	}
	//: define the table.
	return p.openTable(parent, last, at)
}

// walkHeader follows the intermediate parts of a header from the root,
// creating the tables they imply, and returns the table the last part belongs
// to.
func (p *parser) walkHeader(parts []keyPart) (int32, error) {
	t := rootNode
	//: each intermediate part must be, or become, a table.
	for _, part := range parts {
		next, err := p.headerStep(t, part)
		//: the part names a value.
		if err != nil {
			//: refused.
			return noNode, err
		}
		t = next
	}
	//: the parent of the header's own table.
	return t, nil
}

// headerStep returns the table part names in t, creating it if it does not
// exist. A header may pass through any table but an inline one, and through
// an array of tables into its last element.
func (p *parser) headerStep(t int32, part keyPart) (int32, error) {
	c := p.child(t, p.partBytes(part))
	//: an absent part is implied by the header.
	if c == noNode {
		//: created, and still definable by its own header later.
		return p.newTable(t, part, originImplicit)
	}
	n := &p.nodes[c]
	//: a table may be passed through, unless it is inline and so closed.
	if n.kind == kindTable && n.origin != originInline {
		//: descend.
		return c, nil
	}
	//: an array of tables is passed through its last element.
	if n.kind == kindArrayOfTables {
		//: the element most recently appended.
		return n.last, nil
	}
	//: a value, a static array or an inline table cannot be extended.
	return noNode, p.fail(int(part.at), problemExtendValue)
}

// openTable defines the table a [header] names.
func (p *parser) openTable(parent int32, part keyPart, at int) error {
	c := p.child(parent, p.partBytes(part))
	//: the first mention of the table defines it.
	if c == noNode {
		t, err := p.newTable(parent, part, originHeader)
		p.current = t
		//: nil, or the depth refusal.
		return err
	}
	n := &p.nodes[c]
	//: a table a longer header implied may be defined once by its own.
	if n.kind == kindTable && n.origin == originImplicit {
		n.origin = originHeader
		p.current = c
		//: defined.
		return nil
	}
	//: anything else is a redefinition (§Table: "you cannot define a table
	//: more than once"), a table a dotted key defined, an array of tables or
	//: a value.
	return p.fail(at, redefinitionProblem(n))
}

// openArrayTable appends a new element to the array of tables a [[header]]
// names.
func (p *parser) openArrayTable(parent int32, part keyPart, at int) error {
	c := p.child(parent, p.partBytes(part))
	//: the first [[header]] creates the array.
	if c == noNode {
		//: the array lives at the depth of a table.
		if p.nodes[parent].depth >= maxDepth {
			//: refused.
			return p.fail(at, problemTooDeep)
		}
		n := node{kind: kindArrayOfTables, key: part.name, at: int32(at)}
		//: the key's bytes live where the key parser put them.
		if part.escaped {
			n.flags |= flagKeyEscaped
		}
		c = p.add(parent, n)
	} else if p.nodes[c].kind != kindArrayOfTables {
		//: a table, a static array or a value of that name already exists
		//: (§Array of Tables).
		return p.fail(at, problemNotArrayOfTables)
	}
	element, err := p.newTable(c, keyPart{at: int32(at)}, originElement)
	p.current = element
	//: nil, or the depth refusal.
	return err
}

// redefinitionProblem names why a [header] cannot define the existing node n.
func redefinitionProblem(n *node) string {
	switch {
	//: the table already has its header.
	case n.kind == kindTable && n.origin == originHeader:
		//: twice.
		return problemTableTwice
	//: dotted keys defined it.
	case n.kind == kindTable && n.origin == originDotted:
		//: dotted keys cannot be reopened by a header.
		return problemTableDotted
	//: it is an array of tables.
	case n.kind == kindArrayOfTables:
		//: [x] after [[x]].
		return problemTableIsArray
	//: an inline table, a static array, or a scalar.
	default:
		//: a value.
		return problemExtendValue
	}
}

// keyValue reads `key = value` and adds the value to table t.
func (p *parser) keyValue(t int32) error {
	at := p.pos
	//: the key, possibly dotted.
	if err := p.key(); err != nil {
		//: not a key.
		return err
	}
	//: the separator.
	if !p.consume(charEquals) {
		//: a key with no value.
		return p.fail(p.pos, problemExpectedEquals)
	}
	p.skipSpace()
	// The value may contain an inline table whose own keys reuse p.parts, so
	// the intermediate parts are walked and the last one copied first.
	parent, err := p.walkDotted(t, p.parts[:len(p.parts)-1])
	//: an intermediate part names something a dotted key cannot extend.
	if err != nil {
		//: refused.
		return err
	}
	last := p.parts[len(p.parts)-1]
	//: a key is defined once (§Keys: "Defining a key multiple times is invalid").
	if p.child(parent, p.partBytes(last)) != noNode {
		//: refused where the key-value starts.
		return p.fail(at, problemKeyTwice)
	}
	//: the value, as the last part's child of the parent table.
	return p.value(parent, last)
}

// walkDotted follows the intermediate parts of a dotted key from table t,
// creating the tables they define, and returns the table the last part
// belongs to.
func (p *parser) walkDotted(t int32, parts []keyPart) (int32, error) {
	//: each intermediate part must be, or become, a dotted-key table.
	for _, part := range parts {
		c := p.child(t, p.partBytes(part))
		//: an absent part is defined by the dotted key.
		if c == noNode {
			next, err := p.newTable(t, part, originDotted)
			//: the depth refusal.
			if err != nil {
				//: refused.
				return noNode, err
			}
			t = next
			continue
		}
		//: only a table dotted keys created may be extended by one (§Keys,
		//: §Table: a table a header defined, or implied, is not extended by
		//: a dotted key from another section).
		if p.nodes[c].kind != kindTable || p.nodes[c].origin != originDotted {
			//: refused.
			return noNode, p.fail(int(part.at), problemDottedExtends)
		}
		t = c
	}
	//: the table the last part belongs to.
	return t, nil
}

// consume advances over c if it is the next byte, and reports whether it was.
func (p *parser) consume(c byte) bool {
	//: only the expected byte is consumed.
	if p.pos < len(p.data) && p.data[p.pos] == c {
		p.pos++
		//: consumed.
		return true
	}
	//: something else, or the end.
	return false
}

// key reads a simple or dotted key into p.parts, and the whitespace after it.
func (p *parser) key() error {
	p.parts = p.parts[:0]
	//: one part, then another after each dot.
	for {
		part, err := p.simpleKey()
		//: not a key.
		if err != nil {
			//: refused.
			return err
		}
		p.parts = append(p.parts, part)
		p.skipSpace()
		//: no dot: the key is complete.
		if !p.consume(charDot) {
			//: done.
			return nil
		}
		p.skipSpace()
	}
}

// simpleKey reads one part of a key: bare, or a quoted basic or literal
// string on one line.
func (p *parser) simpleKey() (keyPart, error) {
	at := int32(p.pos)
	//: nothing left where a key is required.
	if p.pos >= len(p.data) {
		//: refused.
		return keyPart{}, p.fail(p.pos, problemExpectedKey)
	}
	//: a quoted key is a single-line string.
	if c := p.data[p.pos]; c == charQuote || c == charApostrophe {
		name, escaped, err := p.singleLineString()
		//: nil, or the string's refusal.
		return keyPart{name: name, at: at, escaped: escaped}, err
	}
	start := p.pos
	//: a bare key is a run of A-Z a-z 0-9 - _.
	for p.pos < len(p.data) && isBareKeyByte(p.data[p.pos]) {
		p.pos++
	}
	//: an empty bare key is no key at all.
	if p.pos == start {
		//: refused.
		return keyPart{}, p.fail(p.pos, problemExpectedKey)
	}
	//: the bare key, in the document's bytes.
	return keyPart{name: span{start: int32(start), end: int32(p.pos)}, at: at}, nil
}

// isBareKeyByte reports whether c may appear in a bare key.
func isBareKeyByte(c byte) bool {
	//: ASCII letters, digits, the hyphen and the underscore (§Keys).
	return byteClass[c]&classBareKey != 0
}
