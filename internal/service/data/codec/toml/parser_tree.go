package toml

import (
	"bytes"
	"hash/maphash"
)

// The kinds of node a document is made of.
const (
	// kindTable is a table: the root, a [header], an intermediate a header or
	// a dotted key implied, an inline table, or one element of an array of
	// tables.
	kindTable kind = iota + 1
	// kindArrayOfTables is the array a [[header]] appends to.
	kindArrayOfTables
	// kindArray is an array value, written with brackets.
	kindArray
	// kindString is a string of any of the four spellings.
	kindString
	// kindInteger is a 64-bit signed integer.
	kindInteger
	// kindFloat is an IEEE 754 binary64 value.
	kindFloat
	// kindBool is true or false.
	kindBool
	// kindDateTime is an offset date-time: an instant.
	kindDateTime
	// kindLocalDateTime is a date-time with no offset.
	kindLocalDateTime
	// kindLocalDate is a calendar day.
	kindLocalDate
	// kindLocalTime is a time of day.
	kindLocalTime
)

// The ways a table comes to exist.
const (
	// originImplicit is a table a longer [header] or [[header]] implied: its
	// own [header] may still define it, once.
	originImplicit origin = iota + 1
	// originHeader is a table its own [header] defined, or the root.
	originHeader
	// originDotted is a table a dotted key created: more dotted keys of the
	// same section may extend it, and a [header] may add a sub-table to it,
	// but nothing may define it again.
	originDotted
	// originInline is an inline table: complete when its closing brace is
	// read, and never extended afterwards.
	originInline
	// originElement is one element of an array of tables.
	originElement
)

// Flags a node carries.
const (
	// flagKeyEscaped says the node's key lives in the unescape buffer.
	flagKeyEscaped uint8 = 1 << iota
	// flagTextEscaped says the node's text lives in the unescape buffer.
	flagTextEscaped
	// flagIndexed says the table's children are in the hash index.
	flagIndexed
)

// noNode marks the absence of a node in a link.
const noNode int32 = -1

// rootNode is the index of the root table in every arena.
const rootNode int32 = 0

// linearLimit is the child count up to which a table is searched by a scan of
// its children. A wider table moves to the hash index, so that a document
// with a hundred thousand keys in one table costs a hundred thousand lookups
// and not five billion comparisons.
const linearLimit int32 = 16

// hashMixer spreads a table's index over the bits of a key's hash, so the same
// key in two tables lands in two buckets.
const hashMixer uint64 = 0x9E3779B97F4A7C15

// hashSeed seeds the hash index. One seed per process is enough: the index
// only has to spread keys, and a collision costs a comparison, not a wrong
// answer.
var hashSeed = maphash.MakeSeed()

// kind is what a node holds.
type kind uint8

// origin is how a table came to exist, which decides what a later line of the
// document may still add to it (TOML v1.0.0 §Table, §Inline Table, §Array of
// Tables).
type origin uint8

// span is a run of bytes, located by offsets so that it stays valid while the
// buffer it points into grows. Whether it points into the document or into
// the unescape buffer is recorded by the node that owns it.
type span struct {
	// start is the offset of the first byte.
	start int32
	// end is the offset after the last byte.
	end int32
}

// node is one table, array or value of a document. A node's children are a
// linked list, so that appending to a table or an array never moves another
// node.
type node struct {
	// num is the value of an integer (as int64 bits), a float (as float64
	// bits) or a boolean (0 or 1), or the index of a date-time in the
	// parser's date-time list.
	num uint64
	// key is the name the node has in its parent table; empty for an array
	// element and for the root.
	key span
	// text is a string's value, or the token a scalar was written as.
	text span
	// parent is the table or array that holds the node.
	parent int32
	// first is the first child of a table or an array.
	first int32
	// last is the last child of a table or an array.
	last int32
	// next is the next child of the same parent.
	next int32
	// hnext is the next node in the same bucket of the hash index.
	hnext int32
	// count is how many children a table or an array has.
	count int32
	// at is the offset in the document where the node was written, for a
	// diagnostic.
	at int32
	// depth is how many tables and arrays enclose the node.
	depth int32
	// kind is what the node holds.
	kind kind
	// origin is how a table came to exist.
	origin origin
	// flags are flagKeyEscaped, flagTextEscaped and flagIndexed.
	flags uint8
}

// keyPart is one dotted part of a key, as the key parser read it.
type keyPart struct {
	// name is the unescaped part.
	name span
	// at is where the part starts in the document.
	at int32
	// escaped says name lives in the unescape buffer.
	escaped bool
}

// keyBytes returns the key of node n.
func (p *parser) keyBytes(n *node) []byte {
	//: an escaped key lives in the unescape buffer.
	if n.flags&flagKeyEscaped != 0 {
		//: the unescaped bytes.
		return p.unescaped[n.key.start:n.key.end]
	}
	//: the document's own bytes.
	return p.data[n.key.start:n.key.end]
}

// textBytes returns the text of node n.
func (p *parser) textBytes(n *node) []byte {
	//: an escaped string lives in the unescape buffer.
	if n.flags&flagTextEscaped != 0 {
		//: the unescaped bytes.
		return p.unescaped[n.text.start:n.text.end]
	}
	//: the document's own bytes.
	return p.data[n.text.start:n.text.end]
}

// partBytes returns the bytes of one key part.
func (p *parser) partBytes(part keyPart) []byte {
	//: an escaped part lives in the unescape buffer.
	if part.escaped {
		//: the unescaped bytes.
		return p.unescaped[part.name.start:part.name.end]
	}
	//: the document's own bytes.
	return p.data[part.name.start:part.name.end]
}

// child returns the child of table t named key, or noNode.
func (p *parser) child(t int32, key []byte) int32 {
	//: a wide table is searched through the hash index.
	if p.nodes[t].flags&flagIndexed != 0 {
		//: one bucket, then a comparison per collision.
		return p.indexLookup(t, key)
	}
	//: a narrow table is scanned.
	for c := p.nodes[t].first; c != noNode; c = p.nodes[c].next {
		//: the first child with that key is the only one.
		if bytes.Equal(p.keyBytes(&p.nodes[c]), key) {
			//: found.
			return c
		}
	}
	//: no such key.
	return noNode
}

// add appends n to the arena as the last child of parent and returns its
// index. The caller has checked the depth and the key.
func (p *parser) add(parent int32, n node) int32 {
	idx := int32(len(p.nodes))
	n.parent, n.first, n.last, n.next, n.hnext = parent, noNode, noNode, noNode, noNode
	n.depth = p.nodes[parent].depth + 1
	p.nodes = append(p.nodes, n)
	holder := &p.nodes[parent]
	//: the first child starts the list; any other follows the last.
	if holder.last == noNode {
		holder.first = idx
	} else {
		p.nodes[holder.last].next = idx
	}
	holder.last = idx
	holder.count++
	//: a table already in the index indexes every new key.
	if holder.flags&flagIndexed != 0 {
		p.indexInsert(idx)
	} else if holder.kind == kindTable && holder.count > linearLimit {
		//: a table that just became wide moves to the index.
		p.indexTable(parent)
	}
	//: the new node.
	return idx
}

// keyHash hashes key in table t.
func keyHash(t int32, key []byte) uint64 {
	//: the key's hash, spread by the table so equal keys of different tables part.
	return maphash.Bytes(hashSeed, key) ^ (uint64(t) * hashMixer)
}

// indexTable moves every child of table t into the hash index.
func (p *parser) indexTable(t int32) {
	//: lazily created: most documents never have a wide table.
	if p.index == nil {
		p.index = make(map[uint64]int32)
	}
	p.nodes[t].flags |= flagIndexed
	//: each child, in order.
	for c := p.nodes[t].first; c != noNode; c = p.nodes[c].next {
		p.indexInsert(c)
	}
}

// indexInsert puts node c in the bucket of its key.
func (p *parser) indexInsert(c int32) {
	n := &p.nodes[c]
	h := keyHash(n.parent, p.keyBytes(n))
	head, ok := p.index[h]
	//: an empty bucket has no chain to continue.
	if !ok {
		head = noNode
	}
	n.hnext = head
	p.index[h] = c
}

// indexLookup finds the child of table t named key through the hash index.
func (p *parser) indexLookup(t int32, key []byte) int32 {
	c, ok := p.index[keyHash(t, key)]
	//: an empty bucket holds no such key.
	if !ok {
		//: absent.
		return noNode
	}
	//: walk the bucket: a collision is another table or another key.
	for ; c != noNode; c = p.nodes[c].hnext {
		n := &p.nodes[c]
		//: both the table and the key must match.
		if n.parent == t && bytes.Equal(p.keyBytes(n), key) {
			//: found.
			return c
		}
	}
	//: absent.
	return noNode
}
