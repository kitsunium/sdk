// Package yaml — the parser: its state, its cursor, and one document.
package yaml

import (
	"strings"
	"unicode/utf8"

	"github.com/kitsunium/sdk/internal/kernel/concur/recycler"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// byteOrderMark is the UTF-8 encoding of U+FEFF, which may open a stream.
const byteOrderMark string = "\ufeff"

// noNode is the index parseDocument returns for a document that holds no
// node at all: an empty input, or one of comments only.
const noNode int32 = -1

// smallMapping is the number of entries up to which duplicate keys are found
// by comparing every pair: below it, the comparisons cost less than a set.
const smallMapping int32 = 16

// parserPool recycles parsers and their arenas across calls. A parser whose
// arena grew past maxPooledNodes is dropped rather than repooled.
var parserPool = recycler.NewCappedPool[*parser](
	func() *parser { return &parser{} },
	func(p *parser) { p.reset() },
	func(p *parser) int { return cap(p.nodes) },
	maxPooledNodes,
)

// parser reads one document of the subset into an arena of nodes. It is used
// by one goroutine at a time and recycled through parserPool.
type parser struct {
	// dup is the scratch set a large mapping's keys are checked against.
	dup map[string]struct{}
	// src is the document, line breaks normalised to "\n".
	src string
	// nodes is the arena; a node is addressed by its index.
	nodes []node
	// kids holds every collection's children, each collection's contiguous.
	kids []int32
	// stack collects the children of the collections being parsed.
	stack []int32
	// text is the scratch a scalar with escapes or folding is built in.
	text []byte
	// pos is the byte offset of the cursor.
	pos int
	// line is the 1-based line of the cursor.
	line int
	// lineStart is the byte offset where the cursor's line starts.
	lineStart int
	// depth is how many collections enclose the cursor.
	depth int
}

// acquireParser returns a parser reset to read src.
func acquireParser(src string) *parser {
	p := parserPool.Get()
	//: a byte order mark may open the document; it is not part of line 1.
	p.src = strings.TrimPrefix(src, byteOrderMark)
	p.line = 1
	//: ready.
	return p
}

// releaseParser hands p back to the pool.
func releaseParser(p *parser) {
	//: reset happens in the pool, or the parser is dropped.
	parserPool.Put(p)
}

// reset empties the parser for reuse. The used part of the arena is cleared
// first, so a pooled parser keeps no reference to the document it read.
func (p *parser) reset() {
	clear(p.nodes)
	p.nodes = p.nodes[:0]
	p.kids = p.kids[:0]
	p.stack = p.stack[:0]
	p.text = p.text[:0]
	clear(p.dup)
	p.src = ""
	p.pos, p.line, p.lineStart, p.depth = 0, 1, 0, 0
}

// at returns the byte at i, or 0 past the end. A document never holds a NUL —
// prepareSource refuses control characters — so 0 reads as "the end".
func (p *parser) at(i int) byte {
	//: inside the document.
	if i < len(p.src) {
		//: the byte.
		return p.src[i]
	}
	//: the end.
	return 0
}

// peek returns the byte under the cursor, or 0 at the end.
func (p *parser) peek() byte {
	//: the cursor's byte.
	return p.at(p.pos)
}

// isBlank reports whether c separates tokens on a line: a space or a tab.
func isBlank(c byte) bool {
	//: YAML's s-white.
	return c == ' ' || c == '\t'
}

// blankOrEnd reports whether the byte at i ends a token: a blank, a line
// break, or the end of the document.
func (p *parser) blankOrEnd(i int) bool {
	c := p.at(i)
	//: a separator or the end.
	return isBlank(c) || c == '\n' || c == 0
}

// newline moves the cursor past the line break under it.
func (p *parser) newline() {
	p.pos++
	p.line++
	p.lineStart = p.pos
}

// col returns the cursor's 0-based byte column — the indentation arithmetic
// the block structure is decided by.
func (p *parser) col() int {
	//: bytes since the line started.
	return p.pos - p.lineStart
}

// columnOf returns the 1-based column, in characters, of the byte at off,
// reading back to the start of its line. It runs only when an error is
// reported, so the scan costs nothing on the success path.
func (p *parser) columnOf(off int) int {
	start := strings.LastIndexByte(p.src[:off], '\n') + 1
	//: characters before off on its line, plus one.
	return utf8.RuneCountInString(p.src[start:off]) + 1
}

// errHere is a syntax error at the cursor.
func (p *parser) errHere(detail string) error {
	//: the cursor's position.
	return syntaxError(p.line, p.columnOf(p.pos), detail)
}

// errAt is a syntax error at a recorded position.
func (p *parser) errAt(line, off int, detail string) error {
	//: the recorded position.
	return syntaxError(line, p.columnOf(off), detail)
}

// refuseAt is a construct of YAML the subset refuses by name, at a recorded
// position.
func (p *parser) refuseAt(sentinel *errs.Error, line, off int) error {
	//: the construct's sentinel, at its position.
	return refused(sentinel, line, p.columnOf(off))
}

// add appends n to the arena and returns its index, refusing a document past
// maxNodes.
func (p *parser) add(n node) (int32, error) {
	//: the node bound.
	if len(p.nodes) >= maxNodes {
		//: refuse at the node that would cross it.
		return noNode, p.errAt(int(n.line), int(n.off), "the document holds more nodes than the decoder accepts")
	}
	p.nodes = append(p.nodes, n)
	//: the new node's index.
	return int32(len(p.nodes) - 1), nil
}

// scalar adds a scalar node starting at (line, off).
func (p *parser) scalar(value string, style scalarStyle, line, off int) (int32, error) {
	//: a leaf: no children.
	return p.add(node{value: value, off: int32(off), line: int32(line), kind: kindScalar, style: style})
}

// empty adds the empty node a key or an entry has when nothing follows it: a
// plain scalar with no text, which the core schema reads as null.
func (p *parser) empty(line, off int) (int32, error) {
	//: an empty plain scalar.
	return p.scalar("", stylePlain, line, off)
}

// open adds a collection node at the cursor and enters it, refusing nesting
// past maxDepth. Its children are collected on the stack from base onward.
func (p *parser) open(kind nodeKind) (collection int32, base int, err error) {
	//: the depth bound.
	if p.depth >= maxDepth {
		//: refuse at the collection that would cross it.
		return noNode, 0, p.errHere("collections nest deeper than the decoder accepts")
	}
	p.depth++
	collection, err = p.add(node{off: int32(p.pos), line: int32(p.line), kind: kind})
	//: the collection, and where its children start on the stack.
	return collection, len(p.stack), err
}

// close leaves the collection, moving its children from the stack into kids,
// where they stay contiguous.
func (p *parser) close(collection int32, base int) {
	n := &p.nodes[collection]
	n.first = int32(len(p.kids))
	n.count = int32(len(p.stack) - base)
	p.kids = append(p.kids, p.stack[base:]...)
	p.stack = p.stack[:base]
	p.depth--
}

// child returns the j-th child of collection n.
func (p *parser) child(n *node, j int32) int32 {
	//: the children are contiguous from n.first.
	return p.kids[n.first+j]
}

// skipBlanks moves the cursor past spaces and tabs.
func (p *parser) skipBlanks() {
	//: until something else.
	for isBlank(p.peek()) {
		p.pos++
	}
}

// skipToEOL moves the cursor to the line break ending its line, or the end.
func (p *parser) skipToEOL() {
	i := strings.IndexByte(p.src[p.pos:], '\n')
	//: the last line has no break.
	if i < 0 {
		p.pos = len(p.src)
		//: at the end.
		return
	}
	p.pos += i
}

// skipToContent moves the cursor, which is at the start of a line, past every
// line that is empty, blank or a comment, to the start of the next line that
// holds content. It reports false at the end of the document. A tab among a
// line's leading blanks is refused, even on a blank or a comment line: in
// block context the line's start is its indentation, and libyaml — so yaml.v3
// — refuses a tab there, which the subset never reads otherwise.
func (p *parser) skipToContent() (bool, error) {
	//: one line at a time.
	for p.pos < len(p.src) {
		i := p.pos
		//: the line's leading spaces.
		for p.at(i) == ' ' {
			i++
		}
		found, err := p.classifyLine(i)
		//: content, or a tab.
		if found || err != nil {
			//: the verdict.
			return found, err
		}
	}
	//: the end.
	return false, nil
}

// classifyLine looks at the first byte after a line's leading spaces, at i:
// content stops skipToContent with the cursor at the line's start, an empty or
// comment line is skipped, a tab is refused.
func (p *parser) classifyLine(i int) (bool, error) {
	switch p.at(i) {
	//: a tab where the indentation is.
	case '\t':
		//: refused where it is.
		return false, p.errAt(p.line, i, "a tab character cannot indent a line")
	//: a blank last line.
	case 0:
		p.pos = i
	//: an empty or blank line.
	case '\n':
		p.pos = i
		p.newline()
	//: a comment line, whatever its indentation.
	case '#':
		p.pos = i
		p.skipToEOL()
		//: past its line break, when it has one.
		if p.pos < len(p.src) {
			p.newline()
		}
	//: content: the cursor stays at the start of its line.
	default:
		//: found.
		return true, nil
	}
	//: skipped.
	return false, nil
}

// lineIndent returns the indentation of the line the cursor starts, which
// holds content. YAML indents with spaces only: a tab where the indentation
// is read is refused.
func (p *parser) lineIndent() (int, error) {
	n := 0
	//: count the spaces.
	for p.at(p.pos+n) == ' ' {
		n++
	}
	//: a tab before the content.
	if p.at(p.pos+n) == '\t' {
		//: refuse where it is.
		return 0, p.errAt(p.line, p.pos+n, "a tab character cannot indent a line")
	}
	//: the indentation.
	return n, nil
}

// atMarker reports whether the line starting at the cursor is the document
// marker marker ("---" or "..."), followed by a blank or the end of the line.
func (p *parser) atMarker(marker string) bool {
	//: the three characters at column 0, then a separator.
	return p.col() == 0 && strings.HasPrefix(p.src[p.pos:], marker) && p.blankOrEnd(p.pos+len(marker))
}

// atDocumentMarker reports whether the line starting at the cursor is a
// document start or a document end marker.
func (p *parser) atDocumentMarker() bool {
	//: either marker ends whatever node is open.
	return p.atMarker("---") || p.atMarker("...")
}

// finishLine accepts the rest of the line after a node: blanks, an optional
// comment that a blank separates from the node, then the line break or the
// end. The cursor moves to the start of the next line.
func (p *parser) finishLine() error {
	p.skipBlanks()
	//: a comment.
	if p.peek() == '#' {
		//: a # glued to a node is not a comment.
		if p.pos > p.lineStart && !isBlank(p.src[p.pos-1]) {
			//: refuse it.
			return p.errHere("a comment must be separated from content by a blank")
		}
		p.skipToEOL()
	}
	switch p.peek() {
	//: the end of the document.
	case 0:
		//: done.
		return nil
	//: the end of the line.
	case '\n':
		p.newline()
		//: done.
		return nil
	//: a mapping key that did not fit on one line.
	case ':':
		//: refuse it.
		return p.errHere("a mapping key must be written on one line")
	//: anything else.
	default:
		//: refuse it.
		return p.errHere("unexpected content after a value")
	}
}

// parseDocument parses exactly one document and returns its root node, or
// noNode when it holds none. Anything after the document — a second one, a
// directive — is refused by name.
func (p *parser) parseDocument() (int32, error) {
	explicit, err := p.documentStart()
	//: a directive, or content on the --- line.
	if err != nil {
		//: refused.
		return noNode, err
	}
	found, err := p.skipToContent()
	//: a tab where a line is indented.
	if err != nil {
		//: refused.
		return noNode, err
	}
	//: an empty document.
	if !found || p.atDocumentMarker() {
		//: null, nothing, or a refusal.
		return p.emptyDocument(explicit)
	}
	n, err := p.lineIndent()
	//: a tab where the root is indented.
	if err != nil {
		//: refused.
		return noNode, err
	}
	p.pos += n
	root, err := p.parseBlockNode(-1)
	//: the root failed.
	if err != nil {
		//: refused.
		return noNode, err
	}
	//: what follows must be the end.
	return root, p.documentEnd()
}

// emptyDocument ends a document that holds no content: an explicit "---"
// with nothing under it is a null document, an implicit one is nothing, and
// a "..." with no "---" before it closes no document.
func (p *parser) emptyDocument(explicit bool) (int32, error) {
	//: a "..." closing nothing.
	if !explicit && p.atMarker("...") {
		//: refused, as libyaml refuses it.
		return noNode, p.errHere("a document end marker closes no document")
	}
	//: nothing at all.
	if !explicit {
		//: and nothing may follow.
		return noNode, p.documentEnd()
	}
	root, err := p.empty(p.line, p.pos)
	//: the null, unless the arena is full.
	if err != nil {
		//: refused.
		return noNode, err
	}
	//: what follows must be the end.
	return root, p.documentEnd()
}

// documentStart reads what may precede a document's content — blank and
// comment lines, a directive, the "---" marker — and reports whether the
// marker was present. A directive is refused by name; content on the marker
// line is refused.
func (p *parser) documentStart() (explicit bool, err error) {
	found, err := p.skipToContent()
	//: the lines before the content, or a tab among them.
	if !found || err != nil {
		//: an empty document, or refused.
		return false, err
	}
	//: a directive (%YAML, %TAG, or any other).
	if p.peek() == '%' {
		//: refused by name.
		return false, p.refuseAt(DirectiveRefused, p.line, p.pos)
	}
	//: no marker: the document starts with its content.
	if !p.atMarker("---") {
		//: implicit.
		return false, nil
	}
	p.pos += len("---")
	p.skipBlanks()
	//: only a comment may follow the marker on its line.
	if c := p.peek(); c != '\n' && c != '#' && c != 0 {
		//: content on the --- line.
		return false, p.errHere("content on the document start line is outside the supported subset")
	}
	//: the marker's line is done.
	return true, p.finishLine()
}

// documentEnd accepts what may follow a document: blank and comment lines,
// and one "..." marker. A second document — after "---", after "...", or
// a directive opening one — is refused by name.
func (p *parser) documentEnd() error {
	found, err := p.skipToContent()
	//: nothing more, or a tab where a line is indented.
	if !found || err != nil {
		//: the document is the whole input, or refused.
		return err
	}
	//: the end marker closes the document.
	if p.atMarker("...") {
		//: nothing may follow it.
		return p.afterDocumentEnd()
	}
	//: a "---" opens a second document.
	if p.atMarker("---") {
		//: refused by name.
		return p.refuseAt(MultipleDocumentsRefused, p.line, p.pos)
	}
	n, err := p.lineIndent()
	//: a tab where the line is indented.
	if err != nil {
		//: refused.
		return err
	}
	//: content no node of the document can hold — less indented than an
	//: indented root, or after a scalar root.
	return p.errAt(p.line, p.pos+n, "content outside the document's root node")
}

// afterDocumentEnd consumes the "..." under the cursor and refuses anything
// after it but blank and comment lines: it would open another document.
func (p *parser) afterDocumentEnd() error {
	p.pos += len("...")
	//: only a comment may follow it on its line.
	if err := p.finishLine(); err != nil {
		//: content on the ... line.
		return err
	}
	found, err := p.skipToContent()
	//: nothing after it, or a tab where a line is indented.
	if !found || err != nil {
		//: done, or refused.
		return err
	}
	//: anything after it opens another document.
	return p.refuseAt(MultipleDocumentsRefused, p.line, p.pos)
}

// checkDuplicates refuses a mapping that holds the same key twice. Keys
// compare by their text, whatever their style: "a" and a are one key here, as
// they are to a person reading the file.
func (p *parser) checkDuplicates(collection int32) error {
	n := &p.nodes[collection]
	pairs := n.count / 2
	//: a small mapping is checked pairwise, without a set.
	if pairs <= smallMapping {
		//: every key against the ones before it.
		return p.checkDuplicatesPairwise(n, pairs)
	}
	//: a large mapping is checked against a set, reused across mappings.
	if p.dup == nil {
		p.dup = make(map[string]struct{}, pairs)
	}
	clear(p.dup)
	//: every key.
	for i := range pairs {
		key := &p.nodes[p.child(n, 2*i)]
		//: seen before.
		if _, seen := p.dup[key.value]; seen {
			//: refused at the second occurrence.
			return p.refuseAt(DuplicateKey, int(key.line), int(key.off))
		}
		p.dup[key.value] = struct{}{}
	}
	//: no duplicate.
	return nil
}

// checkDuplicatesPairwise compares every key of the mapping n with the ones
// before it.
func (p *parser) checkDuplicatesPairwise(n *node, pairs int32) error {
	//: every key after the first.
	for i := int32(1); i < pairs; i++ {
		key := &p.nodes[p.child(n, 2*i)]
		//: the earlier keys.
		for j := range i {
			//: the same text.
			if p.nodes[p.child(n, 2*j)].value == key.value {
				//: refused at the second occurrence.
				return p.refuseAt(DuplicateKey, int(key.line), int(key.off))
			}
		}
	}
	//: no duplicate.
	return nil
}
