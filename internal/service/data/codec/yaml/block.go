// Package yaml — the block structure: mappings, sequences, keys and values.
package yaml

import (
	"strings"
	"unicode/utf8"

	coreyaml "github.com/kitsunium/sdk/internal/core/data/codec/yaml"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// noKeyStart lists the indicators no implicit key starts with: the node
// parser refuses each by name or as a syntax error.
const noKeyStart string = "&*!%@`[{]},#|>"

// nodeIndicatorRefusals maps the indicators that open a refused construct to
// the sentinel naming it.
var nodeIndicatorRefusals = map[byte]*errs.Error{
	'&': coreyaml.AnchorRefused,
	'*': coreyaml.AliasRefused,
	'!': coreyaml.TagRefused,
}

// isEntry reports whether the byte at i starts a block sequence entry: a "-"
// followed by a blank or the end of its line.
func (p *parser) isEntry(i int) bool {
	//: the indicator and its separator.
	return p.at(i) == '-' && p.blankOrEnd(i+1)
}

// parseBlockNode parses the node whose first character is under the cursor,
// in block context. parent is the indentation of the collection holding the
// node: -1 at the root.
func (p *parser) parseBlockNode(parent int) (int32, error) {
	switch c := p.peek(); {
	//: a block sequence.
	case p.isEntry(p.pos):
		//: its entries sit at this column.
		return p.parseBlockSequence(p.col())
	//: a block scalar.
	case c == '|' || c == '>':
		//: literal or folded.
		return p.parseBlockScalar(parent)
	//: a block mapping, when the line holds an implicit key.
	case p.lineHasImplicitKey():
		//: its keys sit at this column.
		return p.parseBlockMapping(p.col())
	//: a scalar or a flow collection.
	default:
		//: on this line.
		return p.parseInlineNode(parent)
	}
}

// parseBlockMapping parses a block mapping whose first key is under the
// cursor at column indent; every later key sits at the same column on a line
// of its own.
func (p *parser) parseBlockMapping(indent int) (int32, error) {
	collection, base, err := p.open(kindMapping)
	//: the depth or node bound.
	if err != nil {
		//: refused.
		return noNode, err
	}
	//: one entry per iteration.
	for more := true; more; {
		//: the key and its value.
		if err := p.parseBlockEntry(indent); err != nil {
			//: refused.
			return noNode, err
		}
		more, err = p.nextEntry(indent)
		//: misplaced content.
		if err != nil {
			//: refused.
			return noNode, err
		}
		//: a sequence entry where a key belongs.
		if more && p.isEntry(p.pos) {
			//: refused.
			return noNode, p.errHere("a sequence entry where a mapping key was expected")
		}
	}
	p.close(collection, base)
	//: one key, one value.
	return collection, p.checkDuplicates(collection)
}

// parseBlockEntry parses one entry of a block mapping — its key and its value
// — onto the stack.
func (p *parser) parseBlockEntry(indent int) error {
	key, err := p.parseImplicitKey()
	//: a key that is not one.
	if err != nil {
		//: refused.
		return err
	}
	value, err := p.parseBlockValue(indent, true)
	//: the value failed.
	if err != nil {
		//: refused.
		return err
	}
	p.stack = append(p.stack, key, value)
	//: one entry.
	return nil
}

// nextEntry moves to the next content line and reports whether it continues
// the block collection whose entries sit at column indent; when it does, the
// cursor is left on the entry. A line indented deeper than the entries is
// refused: nothing of the previous entry can still be open.
func (p *parser) nextEntry(indent int) (bool, error) {
	found, err := p.skipToContent()
	//: the end, or a document marker, ends every collection.
	if !found || err != nil || p.atDocumentMarker() {
		//: done, or refused.
		return false, err
	}
	n, err := p.lineIndent()
	//: a tab where the line is indented.
	if err != nil {
		//: refused.
		return false, err
	}
	switch {
	//: a shallower line belongs to an enclosing collection.
	case n < indent:
		//: done; the cursor stays at the line's start.
		return false, nil
	//: a deeper line belongs to nothing.
	case n > indent:
		//: refused where it starts.
		return false, p.errAt(p.line, p.pos+n, "unexpected indentation")
	//: the next entry.
	default:
		p.pos += n
		//: continue.
		return true, nil
	}
}

// parseBlockSequence parses a block sequence whose first "-" is under the
// cursor at column indent.
func (p *parser) parseBlockSequence(indent int) (int32, error) {
	collection, base, err := p.open(kindSequence)
	//: the depth or node bound.
	if err != nil {
		//: refused.
		return noNode, err
	}
	//: one entry per iteration, while the next line is one.
	for more := true; more; {
		p.pos++
		entry, eerr := p.parseBlockValue(indent, false)
		//: the entry failed.
		if eerr != nil {
			//: refused.
			return noNode, eerr
		}
		p.stack = append(p.stack, entry)
		more, err = p.nextSequenceEntry(indent)
		//: misplaced content.
		if err != nil {
			//: refused.
			return noNode, err
		}
	}
	p.close(collection, base)
	//: the sequence.
	return collection, nil
}

// nextSequenceEntry reports whether the next content line is another entry of
// the block sequence at column indent. A line at that column that is not an
// entry belongs to the mapping an indentless sequence is the value of: the
// cursor goes back to its start.
func (p *parser) nextSequenceEntry(indent int) (bool, error) {
	more, err := p.nextEntry(indent)
	//: the end, a marker, a shallower line, or a refusal.
	if !more || err != nil {
		//: done.
		return false, err
	}
	//: another entry.
	if p.isEntry(p.pos) {
		//: continue.
		return true, nil
	}
	p.pos = p.lineStart
	//: done.
	return false, nil
}

// parseBlockValue parses what follows a mapping key's ":" or a sequence
// entry's "-": a node on the same line, a node on the lines below indented
// deeper than parent, or nothing — the empty node. mapValue says which.
func (p *parser) parseBlockValue(parent int, mapValue bool) (int32, error) {
	line, off := p.line, p.pos
	//: after "-", only spaces: libyaml refuses a tab there, and the subset
	//: reads nothing libyaml refuses.
	if err := p.entrySeparator(mapValue); err != nil {
		//: refused.
		return noNode, err
	}
	p.skipBlanks()
	//: something on this line.
	if c := p.peek(); c != '\n' && c != '#' && c != 0 {
		//: the value starts here.
		return p.parseValueOnLine(parent, mapValue)
	}
	//: the comment, and the line break.
	if err := p.finishLine(); err != nil {
		//: cannot happen after a blank, but stays a refusal.
		return noNode, err
	}
	//: what the following lines hold.
	return p.parseValueBelow(parent, mapValue, line, off)
}

// entrySeparator refuses a tab after a sequence entry's "-": libyaml reads
// the start of an entry as indentation.
func (p *parser) entrySeparator(mapValue bool) error {
	//: a mapping value may follow a tab.
	if mapValue {
		//: nothing to check.
		return nil
	}
	//: the spaces.
	for p.peek() == ' ' {
		p.pos++
	}
	//: a tab.
	if p.peek() == '\t' {
		//: refused where it is.
		return p.errHere("a tab character cannot follow a sequence entry's '-'")
	}
	//: spaces only.
	return nil
}

// parseValueOnLine parses a value that starts on the line of its key or its
// "-". A mapping value may not be a block sequence or a mapping on that line;
// a sequence entry may hold either, compact.
func (p *parser) parseValueOnLine(parent int, mapValue bool) (int32, error) {
	//: a block scalar.
	if c := p.peek(); c == '|' || c == '>' {
		//: literal or folded.
		return p.parseBlockScalar(parent)
	}
	//: after a key.
	if mapValue {
		//: a scalar or a flow collection only.
		return p.parseMapValueOnLine(parent)
	}
	//: after a "-".
	return p.parseEntryOnLine(parent)
}

// parseMapValueOnLine parses a mapping value on the line of its key: a block
// collection cannot start there.
func (p *parser) parseMapValueOnLine(parent int) (int32, error) {
	switch {
	//: a sequence on the line of its key.
	case p.isEntry(p.pos):
		//: refused.
		return noNode, p.errHere("a block sequence cannot start on the line of its key")
	//: a mapping on the line of its key.
	case p.lineHasImplicitKey():
		//: refused, as YAML does.
		return noNode, p.errHere("a mapping value cannot be a mapping written on the same line")
	//: a scalar or a flow collection.
	default:
		//: on this line.
		return p.parseInlineNode(parent)
	}
}

// parseEntryOnLine parses a sequence entry on the line of its "-": a compact
// sequence, a compact mapping, a scalar or a flow collection.
func (p *parser) parseEntryOnLine(parent int) (int32, error) {
	switch {
	//: a compact sequence: its entries sit at this column.
	case p.isEntry(p.pos):
		//: the sequence.
		return p.parseBlockSequence(p.col())
	//: a compact mapping: its keys sit at this column.
	case p.lineHasImplicitKey():
		//: the mapping.
		return p.parseBlockMapping(p.col())
	//: a scalar or a flow collection.
	default:
		//: on this line.
		return p.parseInlineNode(parent)
	}
}

// parseValueBelow parses a value written on the lines after its key or its
// "-": a node indented deeper than parent, an indentless sequence at the key's
// own indentation, or nothing.
func (p *parser) parseValueBelow(parent int, mapValue bool, line, off int) (int32, error) {
	found, err := p.skipToContent()
	//: a tab where a line is indented.
	if err != nil {
		//: refused.
		return noNode, err
	}
	//: the end, or a document marker: no value.
	if !found || p.atDocumentMarker() {
		//: the empty node.
		return p.empty(line, off)
	}
	n, err := p.lineIndent()
	//: a tab where the line is indented.
	if err != nil {
		//: refused.
		return noNode, err
	}
	switch {
	//: a nested node.
	case n > parent:
		p.pos += n
		//: whatever it is.
		return p.parseBlockNode(parent)
	//: a sequence at the indentation of its key.
	case mapValue && n == parent && p.isEntry(p.pos+n):
		p.pos += n
		//: an indentless sequence.
		return p.parseBlockSequence(n)
	//: the next line belongs to an enclosing collection.
	default:
		//: the empty node; the cursor stays at the line's start.
		return p.empty(line, off)
	}
}

// parseImplicitKey parses the mapping key under the cursor, through the ":"
// that ends it. A key is a scalar on one line; anything else is refused by
// name.
func (p *parser) parseImplicitKey() (int32, error) {
	line, off := p.line, p.pos
	//: an anchor, an alias, a tag, a "?" — refused by name.
	if err := p.refuseNodeIndicator(false); err != nil {
		//: refused.
		return noNode, err
	}
	value, style, err := p.scanBlockKey(line, off)
	//: the key failed.
	if err != nil {
		//: refused.
		return noNode, err
	}
	//: the ":" that makes it a key.
	if err := p.keyIndicator(line, off, style, value, false); err != nil {
		//: refused.
		return noNode, err
	}
	//: the key node.
	return p.scalar(value, style, line, off)
}

// scanBlockKey scans a block mapping key: double-quoted, single-quoted or
// plain, on one line. A flow collection as a key is refused by name.
func (p *parser) scanBlockKey(line, off int) (string, scalarStyle, error) {
	switch p.peek() {
	//: a double-quoted key.
	case '"':
		value, err := p.scanDoubleQuoted(true)
		//: the key.
		return value, styleDouble, err
	//: a single-quoted key.
	case '\'':
		value, err := p.scanSingleQuoted(true)
		//: the key.
		return value, styleSingle, err
	//: a flow collection as a key.
	case '[', '{':
		//: a complex key.
		return "", stylePlain, p.refuseAt(coreyaml.ComplexKeyRefused, line, off)
	//: a plain key.
	default:
		value, err := p.scanPlainKey(false)
		//: the key.
		return value, stylePlain, err
	}
}

// keyIndicator checks the key just scanned — its length, the merge key — and
// moves past the ":" after it. In flow context a quoted key may be followed by
// ":" with no blank, as JSON writes it.
func (p *parser) keyIndicator(line, off int, style scalarStyle, value string, flow bool) error {
	p.skipBlanks()
	adjacent := flow && style != stylePlain
	//: the value indicator.
	if p.peek() != ':' || (!p.blankOrEnd(p.pos+1) && !adjacent) {
		//: the key ends without one.
		return p.errHere("a mapping key must be followed by ':'")
	}
	//: the key's bound, and the merge key.
	if err := p.checkKey(line, off, p.pos, style, value); err != nil {
		//: refused.
		return err
	}
	p.pos++
	//: past the indicator.
	return nil
}

// checkKey refuses the key src[off:end]: one longer than YAML's bound on an
// implicit key, or the YAML 1.1 merge key, which copies another mapping.
func (p *parser) checkKey(line, off, end int, style scalarStyle, value string) error {
	//: YAML's bound on an implicit key.
	if utf8.RuneCountInString(p.src[off:end]) > maxKeyRunes {
		//: refused where the key starts.
		return p.errAt(line, off, "a mapping key is longer than 1024 characters")
	}
	//: the merge key.
	if style == stylePlain && value == mergeKey {
		//: refused by name.
		return p.refuseAt(coreyaml.MergeKeyRefused, line, off)
	}
	//: an ordinary key.
	return nil
}

// refuseNodeIndicator refuses, by name, a construct that begins a node: an
// anchor, an alias, a tag, an explicit "?" key, a directive; and a reserved
// indicator.
func (p *parser) refuseNodeIndicator(flow bool) error {
	line, off := p.line, p.pos
	c := p.peek()
	//: &anchor, *alias, !tag.
	if sentinel, ok := nodeIndicatorRefusals[c]; ok {
		//: refused by name.
		return p.refuseAt(sentinel, line, off)
	}
	//: "? key" — an explicit key; in flow context every "?" opens one.
	if c == '?' && (flow || p.blankOrEnd(p.pos+1)) {
		//: refused by name.
		return p.refuseAt(coreyaml.ComplexKeyRefused, line, off)
	}
	//: a directive, or a reserved indicator.
	return p.refuseReserved(c, line, off)
}

// refuseReserved refuses "%" — a directive at column 0, a reserved indicator
// anywhere else — and the reserved indicators "@" and "`".
func (p *parser) refuseReserved(c byte, line, off int) error {
	switch {
	//: % opens a directive at column 0.
	case c == '%' && p.col() == 0:
		//: refused by name.
		return p.refuseAt(coreyaml.DirectiveRefused, line, off)
	//: a plain scalar cannot start with %, @ or `.
	case c == '%' || c == '@' || c == '`':
		//: refused.
		return p.errHere("a plain scalar cannot start with a reserved indicator ('%', '@' or '`')")
	}
	//: an ordinary start.
	return nil
}

// parseInlineNode parses the scalar or flow collection under the cursor and
// the rest of its line.
func (p *parser) parseInlineNode(parent int) (int32, error) {
	//: an anchor, an alias, a tag, a "?" — refused by name.
	if err := p.refuseNodeIndicator(false); err != nil {
		//: refused.
		return noNode, err
	}
	idx, err := p.parseInlineValue(parent)
	//: the node failed.
	if err != nil {
		//: refused.
		return noNode, err
	}
	//: the rest of the line.
	return idx, p.finishLine()
}

// parseInlineValue parses the scalar or flow collection under the cursor.
func (p *parser) parseInlineValue(parent int) (int32, error) {
	line, off := p.line, p.pos
	switch c := p.peek(); c {
	//: a flow collection.
	case '[', '{':
		//: and no ":" after it.
		return p.parseInlineFlow(parent, line, off)
	//: a double-quoted scalar.
	case '"':
		value, err := p.scanDoubleQuoted(false)
		//: the node.
		return p.scalarOrError(value, styleDouble, line, off, err)
	//: a single-quoted scalar.
	case '\'':
		value, err := p.scanSingleQuoted(false)
		//: the node.
		return p.scalarOrError(value, styleSingle, line, off, err)
	//: a flow indicator out of a flow collection.
	case ']', '}', ',':
		//: refused.
		return noNode, p.errHere("a flow indicator outside a flow collection")
	//: ":" and a blank is a value with no key.
	case ':':
		//: refused when it is the indicator; a plain scalar otherwise.
		if p.blankOrEnd(p.pos + 1) {
			//: refused.
			return noNode, p.errHere("a mapping key cannot be empty")
		}
	}
	value, err := p.scanPlain(parent, false)
	//: a plain scalar.
	return p.scalarOrError(value, stylePlain, line, off, err)
}

// parseInlineFlow parses a flow collection in block context. A collection
// followed by ":" was a key: a complex key, refused by name.
func (p *parser) parseInlineFlow(parent, line, off int) (int32, error) {
	idx, err := p.parseFlowCollection(parent)
	//: the collection failed.
	if err != nil {
		//: refused.
		return noNode, err
	}
	//: a collection used as a key.
	if p.followedByValueIndicator() {
		//: a complex key.
		return noNode, p.refuseAt(coreyaml.ComplexKeyRefused, line, off)
	}
	//: the collection.
	return idx, nil
}

// scalarOrError adds the scalar just scanned, unless its scan failed.
func (p *parser) scalarOrError(value string, style scalarStyle, line, off int, err error) (int32, error) {
	//: the scan failed.
	if err != nil {
		//: refused.
		return noNode, err
	}
	//: the node.
	return p.scalar(value, style, line, off)
}

// followedByValueIndicator reports whether, after blanks, the cursor is on a
// ":" that makes the node before it a mapping key.
func (p *parser) followedByValueIndicator() bool {
	p.skipBlanks()
	//: a ":" and a separator.
	return p.peek() == ':' && p.blankOrEnd(p.pos+1)
}

// lineHasImplicitKey reports, without moving the cursor, whether the line
// from the cursor holds an implicit mapping key: a quoted scalar closed on
// this line, or a plain scalar, followed by ":" and a blank or the end of the
// line.
func (p *parser) lineHasImplicitKey() bool {
	c := p.peek()
	//: a quoted key closes on this line, then the indicator follows.
	if c == '"' || c == '\'' {
		end, ok := p.quotedEnd(p.pos, c)
		//: closed here, and a ":" after it.
		return ok && p.valueIndicatorAfter(end)
	}
	//: indicators no implicit key starts with (a refused construct is refused
	//: by the node parser).
	if c == 0 || strings.IndexByte(noKeyStart, c) >= 0 {
		//: not a key.
		return false
	}
	//: a plain key: a ":" and a blank before the line ends or a comment starts.
	return p.plainKeyEnd(p.pos) >= 0
}

// valueIndicatorAfter reports whether, past blanks from i, a ":" followed by
// a separator sits on the line.
func (p *parser) valueIndicatorAfter(i int) bool {
	//: the blanks.
	for isBlank(p.at(i)) {
		i++
	}
	//: a ":" and a separator.
	return p.at(i) == ':' && p.blankOrEnd(i+1)
}

// plainKeyEnd returns the offset of the ":" ending the plain implicit key
// starting at i, or -1 when the line holds none before it ends or a comment
// starts.
func (p *parser) plainKeyEnd(i int) int {
	start := i
	//: scan the line.
	for ; i < len(p.src); i++ {
		c := p.src[i]
		//: the line ends, or a comment starts.
		if c == '\n' || (c == '#' && i > start && isBlank(p.src[i-1])) {
			//: none.
			return -1
		}
		//: the value indicator.
		if c == ':' && p.blankOrEnd(i+1) {
			//: found.
			return i
		}
	}
	//: the document ends without a key.
	return -1
}

// quotedEnd returns the offset just past the quote closing the quoted scalar
// opened at i, when it closes on the same line.
func (p *parser) quotedEnd(i int, quote byte) (int, bool) {
	//: scan the line.
	for i++; i < len(p.src) && p.src[i] != '\n'; i++ {
		//: the closing quote.
		if p.closesAt(i, quote) {
			//: closed.
			return i + 1, true
		}
		//: an escape, or a doubled single quote, takes two bytes.
		if (p.src[i] == '\\' && quote == '"') || (p.src[i] == '\'' && quote == '\'') {
			i++
		}
	}
	//: the line or the document ends first.
	return 0, false
}
