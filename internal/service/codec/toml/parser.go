// Package toml — the parser: one pass over a TOML v1.0.0 document that
// builds its tree and refuses, as the specification requires, a key or a
// table defined twice, a table extended from a place the specification does
// not allow, and every byte the grammar has no place for.
package toml

import (
	"bytes"
	"unicode/utf8"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/recycler"
)

// maxDocumentBytes caps the size of a document Unmarshal and the streaming
// decoder accept (CWE-400). Ten MiB covers every configuration file there is,
// and matches the cap the YAML and MessagePack codecs apply.
const maxDocumentBytes int = 10 << 20

// maxDepth caps how deep below the root a table, an array or a value may sit
// (CWE-674): a key at the root is at depth 1, a value inside 128 nested arrays
// at depth 129, and refused. The parse and the decode both recurse once per
// level, so the cap is what keeps a document of ten million opening brackets
// from reaching the stack's limit. A configuration nests a handful of levels;
// generated documents a few more.
const maxDepth int32 = 128

// maxRetainedParserBytes is the arena size above which a parser is dropped
// rather than pooled, so that one large document does not pin its arena for
// the life of the pool.
const maxRetainedParserBytes int = 256 << 10

// nodeBytes is the size of one node, for the retain decision.
const nodeBytes int = 64

// Interning: a key is turned into a Go string once per pooled parser, as the
// replaced library did, so a document decoded again — a configuration
// reloaded, a thousand tables with the same five keys — allocates no key twice.
// Only keys are interned: a value may be a secret, and a pooled table outlives
// the call.
const (
	// maxInternedKeys bounds the table; past it, keys are no longer added.
	maxInternedKeys int = 4096
	// maxInternedKeyBytes is the longest key interned.
	maxInternedKeyBytes int = 64
	// internEntryBytes is the table's own cost per key, for the retain
	// decision.
	internEntryBytes int = 32
)

// Presizing: a large document allocates its arena once, sized from the
// separators every node is introduced by, instead of doubling it log2(n)
// times — a 9 000-node document otherwise allocates twice its arena per call,
// since an arena past maxRetainedParserBytes is not pooled.
const (
	// bytesPerNodeGuess is the document size per node below which the arena
	// is assumed big enough and no estimate is made.
	bytesPerNodeGuess int = 16
	// maxPresizedNodes bounds the up-front allocation to 2 MiB of nodes; a
	// document of more nodes grows past it as usual.
	maxPresizedNodes int = 1 << 15
)

// Characters with a role in the grammar.
const (
	// charNewline ends a line.
	charNewline byte = '\n'
	// charReturn may only appear before charNewline.
	charReturn byte = '\r'
	// charHash starts a comment.
	charHash byte = '#'
	// charOpenBracket opens a table header or an array.
	charOpenBracket byte = '['
	// charCloseBracket closes a table header or an array.
	charCloseBracket byte = ']'
	// charOpenBrace opens an inline table.
	charOpenBrace byte = '{'
	// charCloseBrace closes an inline table.
	charCloseBrace byte = '}'
	// charComma separates array elements and inline-table pairs.
	charComma byte = ','
	// charDot separates the parts of a dotted key.
	charDot byte = '.'
	// charEquals separates a key from its value.
	charEquals byte = '='
	// charQuote opens a basic string.
	charQuote byte = '"'
	// charApostrophe opens a literal string.
	charApostrophe byte = '\''
	// charBackslash starts an escape sequence.
	charBackslash byte = '\\'
	// charSpace is whitespace.
	charSpace byte = ' '
	// charTab is whitespace.
	charTab byte = '\t'
	// charDelete is the one control character above the space.
	charDelete byte = 0x7f
	// charFirstPrintable is the first character that is not a control.
	charFirstPrintable byte = 0x20
	// charFirstNonASCII is the first byte of a multi-byte UTF-8 sequence.
	charFirstNonASCII byte = 0x80
)

// parser reads one document into its tree. It is pooled: every slice keeps
// its capacity from one document to the next.
type parser struct {
	// data is the document.
	data []byte
	// nodes is the arena; nodes[0] is the root table.
	nodes []node
	// unescaped holds the strings and keys that had escape sequences.
	unescaped []byte
	// times holds the date-times nodes point to.
	times []datetime
	// parts is the key being read.
	parts []keyPart
	// index finds a key in a wide table: a hash of the table and the key to
	// the first node of its bucket.
	index map[uint64]int32
	// keys interns the keys decoded as Go strings, across documents.
	keys map[string]string
	// internedBytes counts the bytes keys holds, for the retain decision.
	internedBytes int
	// pos is the offset of the next byte to read.
	pos int
	// current is the table key-value lines are added to.
	current int32
}

// parserPool recycles parsers with their arenas, dropping one that grew past
// maxRetainedParserBytes.
var parserPool = recycler.NewCappedPool[*parser](
	func() *parser { return new(parser) },
	(*parser).reset,
	(*parser).retainedBytes,
	maxRetainedParserBytes,
)

// reset empties the parser for the next document, keeping its capacity.
func (p *parser) reset() {
	p.data = nil
	p.nodes = p.nodes[:0]
	p.unescaped = p.unescaped[:0]
	p.times = p.times[:0]
	p.parts = p.parts[:0]
	clear(p.index)
	p.pos = 0
	p.current = rootNode
	//: a full intern table starts over, so it follows the documents in use.
	if len(p.keys) >= maxInternedKeys {
		clear(p.keys)
		p.internedBytes = 0
	}
}

// internKey returns key as a string, the same string every time while the
// parser's table holds it.
func (p *parser) internKey(key []byte) string {
	//: a key seen before costs a lookup, which allocates nothing.
	if s, ok := p.keys[string(key)]; ok {
		//: the shared string.
		return s
	}
	//: a key not seen yet.
	return p.newKey(key)
}

// newKey turns key into a string and keeps it in the intern table when there
// is room for it.
func (p *parser) newKey(key []byte) string {
	s := string(key)
	//: a long key, or a full table, is not kept.
	if len(key) > maxInternedKeyBytes || len(p.keys) >= maxInternedKeys {
		//: a string of its own.
		return s
	}
	//: the table is created on first use.
	if p.keys == nil {
		p.keys = make(map[string]string)
	}
	p.keys[s] = s
	p.internedBytes += len(s) + internEntryBytes
	//: the string, now shared.
	return s
}

// retainedBytes is the memory the parser would keep in the pool.
func (p *parser) retainedBytes() int {
	//: the arena dominates; the other buffers are counted at their size.
	return cap(p.nodes)*nodeBytes + cap(p.unescaped) + cap(p.times)*datetimeBytes + len(p.index)*nodeBytes + p.internedBytes
}

// parse reads data into the parser's tree.
func (p *parser) parse(data []byte) error {
	p.data = data
	p.pos = 0
	//: a document too large for the arena it was handed gets one sized for it.
	if len(data)/bytesPerNodeGuess > cap(p.nodes) {
		p.nodes = make([]node, 0, min(estimateNodes(data), maxPresizedNodes))
	}
	p.nodes = append(p.nodes[:0], node{kind: kindTable, origin: originHeader, parent: noNode, first: noNode, last: noNode, next: noNode, hnext: noNode})
	p.current = rootNode
	//: one expression per line, until the end of the document.
	for p.pos < len(p.data) {
		//: the first refusal ends the parse.
		if err := p.expression(); err != nil {
			//: the refusal, located.
			return err
		}
	}
	//: the whole document is a tree.
	return nil
}

// estimateNodes estimates the nodes data will need: each key-value has its
// '=', each array element its '[' or ',', each header its '['. Separators
// inside strings overcount; dotted keys undercount; both are bounded by the
// caller.
func estimateNodes(data []byte) int {
	//: one pass per separator, each vectorised by the runtime.
	return 1 + bytes.Count(data, []byte{charEquals}) + bytes.Count(data, []byte{charComma}) + bytes.Count(data, []byte{charOpenBracket})
}

// expression reads one line: a table header, a key-value, a comment or
// nothing, then the end of the line.
func (p *parser) expression() error {
	p.skipSpace()
	//: trailing whitespace at the end of the document.
	if p.pos >= len(p.data) {
		//: nothing more to read.
		return nil
	}
	var err error
	switch p.data[p.pos] {
	//: a blank line or a comment: endOfLine consumes it.
	case charNewline, charReturn, charHash:
	//: a [table] or an [[array of tables]].
	case charOpenBracket:
		err = p.header()
	//: anything else must be a key.
	default:
		err = p.keyValue(p.current)
	}
	//: a refusal inside the expression.
	if err != nil {
		//: the refusal, located.
		return err
	}
	//: whitespace, an optional comment, then a newline or the end.
	return p.endOfLine()
}

// endOfLine consumes whitespace, an optional comment and the newline that end
// an expression, or the end of the document.
func (p *parser) endOfLine() error {
	p.skipSpace()
	//: a comment runs to the end of the line.
	if p.pos < len(p.data) && p.data[p.pos] == charHash {
		//: its characters are checked as it is skipped.
		if err := p.comment(); err != nil {
			//: a control character in the comment.
			return err
		}
	}
	//: the end of the document, or a newline, LF or CRLF, ends the line.
	if p.pos >= len(p.data) || p.newline() {
		//: the next expression starts on the next line.
		return nil
	}
	//: anything else after a complete expression.
	return p.fail(p.pos, problemExpectedNewline)
}

// skipSpace advances over spaces and tabs.
func (p *parser) skipSpace() {
	//: whitespace is the space and the tab, nothing else.
	for p.pos < len(p.data) && (p.data[p.pos] == charSpace || p.data[p.pos] == charTab) {
		p.pos++
	}
}

// newline consumes one newline, LF or CRLF, and reports whether there was one.
func (p *parser) newline() bool {
	//: a line feed.
	if p.pos < len(p.data) && p.data[p.pos] == charNewline {
		p.pos++
		//: one LF.
		return true
	}
	//: a carriage return counts only before a line feed.
	if p.pos+1 < len(p.data) && p.data[p.pos] == charReturn && p.data[p.pos+1] == charNewline {
		p.pos += 2
		//: one CRLF.
		return true
	}
	//: no newline here.
	return false
}

// comment consumes a comment up to, not including, its newline, refusing the
// control characters TOML forbids in one (§Comment).
func (p *parser) comment() error {
	p.pos++
	//: until the newline or the end of the document.
	for p.pos < len(p.data) {
		//: printable ASCII and the tab are the common case.
		if byteClass[p.data[p.pos]]&classPrintable != 0 {
			p.pos++
			continue
		}
		//: a newline, LF or CRLF, ends the comment.
		if p.atNewline() {
			//: the newline is the caller's.
			return nil
		}
		//: anything else must be a well-formed non-ASCII character.
		if err := p.skipRune(problemControlInComment); err != nil {
			//: a control character or invalid UTF-8.
			return err
		}
	}
	//: the comment ran to the end of the document.
	return nil
}

// atNewline reports whether a newline, LF or CRLF, starts at p.pos.
func (p *parser) atNewline() bool {
	//: an LF, or a CR before an LF.
	return p.data[p.pos] == charNewline ||
		(p.data[p.pos] == charReturn && p.pos+1 < len(p.data) && p.data[p.pos+1] == charNewline)
}

// skipRune advances over one well-formed UTF-8 character of two or more
// bytes, and refuses anything else with problem.
func (p *parser) skipRune(problem string) error {
	//: an ASCII byte here is a control character the caller did not accept.
	if p.data[p.pos] < charFirstNonASCII {
		//: refused.
		return p.fail(p.pos, problem)
	}
	r, size := utf8.DecodeRune(p.data[p.pos:])
	//: invalid UTF-8: a lone continuation byte, an overlong form, a surrogate.
	if r == utf8.RuneError && size <= 1 {
		//: refused.
		return p.fail(p.pos, problemInvalidUTF8)
	}
	p.pos += size
	//: one character.
	return nil
}

// fail returns UNMARSHAL_FAILED locating problem at offset at. The problem is
// a fixed sentence: no byte of the document is ever copied into an error.
func (p *parser) fail(at int, problem string) error {
	line, column := position(p.data, at)
	//: the sentinel's code and messages, with the location as fields.
	return errs.Wrap(UnmarshalFailed, errs.WrapParams{},
		errs.String(fieldProblem, problem), errs.Int(fieldLine, line), errs.Int(fieldColumn, column))
}

// position returns the 1-based line and column of offset at in data, the
// column counted in characters.
func position(data []byte, at int) (line, column int) {
	at = min(max(at, 0), len(data))
	line, lineStart := 1, 0
	//: count the newlines before the offset.
	for i := range at {
		//: each LF starts a line.
		if data[i] == charNewline {
			line++
			lineStart = i + 1
		}
	}
	//: the line and the characters before the offset on it.
	return line, utf8.RuneCount(data[lineStart:at]) + 1
}

// newTable appends a table to parent and returns it, refusing a depth beyond
// maxDepth.
func (p *parser) newTable(parent int32, part keyPart, from origin) (int32, error) {
	//: the depth cap holds for tables as for values.
	if p.nodes[parent].depth >= maxDepth {
		//: refused where the key was written.
		return noNode, p.fail(int(part.at), problemTooDeep)
	}
	n := node{kind: kindTable, origin: from, key: part.name, at: part.at}
	//: the key's bytes live where the key parser put them.
	if part.escaped {
		n.flags |= flagKeyEscaped
	}
	//: the new table.
	return p.add(parent, n), nil
}
