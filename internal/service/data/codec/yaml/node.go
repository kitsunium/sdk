package yaml

// maxYAMLBytes caps the bytes one document may hold: Unmarshal refuses a
// longer input before reading it, and the streaming decoder refuses a
// document that grows past it. 10 MiB covers every realistic configuration
// document while keeping an attacker-controlled payload from exhausting memory
// (CWE-400).
const maxYAMLBytes int = 10 << 20

// maxDepth bounds how deeply collections nest, when reading and when writing:
// a configuration nested a hundred levels deep is not a configuration, and a
// cyclic Go value would otherwise recurse until the stack ran out.
const maxDepth int = 100

// maxNodes bounds how many nodes one document may hold. The byte cap alone
// lets ten megabytes of "[0,0,0,…]" become five million nodes; this keeps the
// tree a document builds within a fixed multiple of a configuration's size.
const maxNodes int = 1 << 20

// maxKeyRunes is YAML's own bound on an implicit key (YAML 1.2.2 §7.4.2): a key
// written without the ? indicator spans at most 1024 characters.
const maxKeyRunes int = 1024

// mergeKey is YAML 1.1's merge key, refused as a plain key and never written
// plain.
const mergeKey string = "<<"

// maxPooledNodes bounds the arena a recycled parser keeps: a parser that read
// a huge document is dropped instead of pinning its arena in the pool.
const maxPooledNodes int = 1 << 14

// nodeKind is what a parsed node is.
type nodeKind uint8

// The three kinds of node the subset has.
const (
	// kindScalar is a scalar: plain, quoted or block.
	kindScalar nodeKind = iota + 1
	// kindMapping is a block or flow mapping; its children alternate key, value.
	kindMapping
	// kindSequence is a block or flow sequence.
	kindSequence
)

// scalarStyle is how a scalar was written; only a plain scalar is resolved by
// the core schema, every other style is a string.
type scalarStyle uint8

// The scalar styles.
const (
	// stylePlain is an unquoted scalar.
	stylePlain scalarStyle = iota
	// styleSingle is a 'single-quoted' scalar.
	styleSingle
	// styleDouble is a "double-quoted" scalar.
	styleDouble
	// styleBlock is a literal (|) or folded (>) block scalar.
	styleBlock
)

// node is one node of a parsed document, held in the parser's arena and
// addressed by its index.
type node struct {
	// value is a scalar's content, its escapes and folding applied.
	value string
	// off is the byte offset of the node's first character: an error turns it
	// into a column only when one is reported.
	off int32
	// line is the 1-based line of the node's first character.
	line int32
	// first is the index in parser.kids of the node's first child.
	first int32
	// count is how many children the node has: a mapping has two per entry.
	count int32
	// kind is scalar, mapping or sequence.
	kind nodeKind
	// style is how a scalar was written.
	style scalarStyle
}

// isNull reports whether n is a null: an empty node, or a plain ~ or null.
func (n *node) isNull() bool {
	//: only a plain scalar resolves; "null" in quotes is a string.
	return n.kind == kindScalar && n.style == stylePlain && isNullText(n.value)
}

// isNullText reports whether a plain scalar's text is one of the core
// schema's spellings of null.
func isNullText(text string) bool {
	switch text {
	//: the empty node, ~, and null in its three cases.
	case "", "~", "null", "Null", "NULL":
		//: null.
		return true
	//: anything else.
	default:
		//: not null.
		return false
	}
}
