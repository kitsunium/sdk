package bson

import (
	"encoding/binary"
	"unicode/utf8"
)

// The layout Sniff checks a document's head against.
const (
	// emptyDocumentSize is the length of the empty document: its int32 length
	// and its terminating NUL.
	emptyDocumentSize int = 5
	// firstTypeAt is where the first element's type byte sits.
	firstTypeAt int = 4
	// firstKeyAt is where the first element's name starts.
	firstKeyAt int = 5
	// asciiDEL is the one ASCII control byte above the space.
	asciiDEL byte = 0x7F
)

// Sniff implements codec.Sniffer (#909): prefix is BSON when it opens with a
// document's int32 length within the codec's bound, a valid first element type
// and that element's name — UTF-8 without control bytes, NUL-terminated within
// the prefix — and, when the whole document lies in the prefix, its closing
// NUL where the length says. The empty document is its five bytes. It reads
// only prefix and allocates nothing.
func (*bsonCodec) Sniff(prefix []byte) bool {
	//: the length and a type byte at least.
	if len(prefix) < emptyDocumentSize {
		//: too short to say.
		return false
	}
	size := int(binary.LittleEndian.Uint32(prefix))
	//: within the bound, and closed by a NUL when it fits in the prefix.
	if size < emptyDocumentSize || size > maxBSONBytes || (size <= len(prefix) && prefix[size-1] != 0) {
		//: not a document's length.
		return false
	}
	//: the empty document.
	if size == emptyDocumentSize {
		//: its terminating NUL.
		return prefix[firstTypeAt] == 0
	}
	//: the first element: a type, then its name.
	return knownType(prefix[firstTypeAt]) && plausibleKey(prefix[firstKeyAt:min(size, len(prefix))])
}

// knownType reports whether t is a BSON 1.1 element type, the deprecated ones
// included.
func knownType(t byte) bool {
	//: 0x01 to 0x13, MinKey and MaxKey.
	return (t >= typeDouble && t <= typeDecimal128) || t == typeMinKey || t == typeMaxKey
}

// plausibleKey reports whether p opens with an element name: UTF-8 with no
// control byte, ended by a NUL within p.
func plausibleKey(p []byte) bool {
	//: each byte up to the NUL.
	for i, c := range p {
		//: the name ends.
		if c == 0 {
			//: valid as text.
			return utf8.Valid(p[:i])
		}
		//: a control byte names nothing.
		if c < ' ' || c == asciiDEL {
			//: not a name.
			return false
		}
	}
	//: no NUL within the prefix.
	return false
}
