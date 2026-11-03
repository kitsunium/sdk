package xml

import (
	"bytes"
	"unicode/utf8"
)

// The heads an XML document opens with.
var (
	// utf8BOM is the byte order mark a UTF-8 document may open with.
	utf8BOM = []byte{0xEF, 0xBB, 0xBF}
	// declarationHead opens an XML declaration.
	declarationHead = []byte("<?xml")
	// commentHead opens a comment.
	commentHead = []byte("<!--")
	// doctypeHead opens a document type declaration.
	doctypeHead = []byte("<!DOCTYPE")
)

// Sniff implements codec.Sniffer (#909): prefix is XML when, after an optional
// byte order mark and whitespace, it opens with the XML declaration, a
// comment, a DOCTYPE or a start tag — a '<' then a name whose end the prefix
// reaches or shows. It reads only prefix and allocates nothing.
func (*xmlCodec) Sniff(prefix []byte) bool {
	p := bytes.TrimLeft(bytes.TrimPrefix(prefix, utf8BOM), " \t\r\n")
	switch {
	//: <?xml, then whitespace or the end of the prefix.
	case bytes.HasPrefix(p, declarationHead):
		return len(p) == len(declarationHead) || isXMLSpace(p[len(declarationHead)])
	//: a comment or a DOCTYPE.
	case bytes.HasPrefix(p, commentHead), bytes.HasPrefix(p, doctypeHead):
		return true
	//: a start tag.
	default:
		return startTag(p)
	}
}

// startTag reports whether p opens with '<', a name start character, name
// characters, then whitespace, '>', '/' or the end of p.
func startTag(p []byte) bool {
	//: '<' and a name start character.
	if len(p) < 2 || p[0] != '<' || !isNameStart(p[1]) {
		//: no start tag.
		return false
	}
	end := 2 + nameLen(p[2:])
	//: the name ends the prefix, or is followed by what a tag allows.
	return end == len(p) || endsName(p[end])
}

// nameLen returns how many of p's leading bytes are XML name characters.
func nameLen(p []byte) int {
	//: up to the first byte no name holds.
	for i, c := range p {
		if !isNameStart(c) && c != '-' && c != '.' && (c < '0' || c > '9') {
			return i
		}
	}
	//: all of them.
	return len(p)
}

// endsName reports whether c may follow an element's name in a start tag.
func endsName(c byte) bool {
	//: whitespace before an attribute, the tag's end, or an empty tag.
	return isXMLSpace(c) || c == '>' || c == '/'
}

// isNameStart reports whether c may start an XML name: an ASCII letter, '_',
// ':' or the first byte of a non-ASCII character.
func isNameStart(c byte) bool {
	//: the ASCII name start characters, and any multi-byte character.
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == ':' || c >= utf8.RuneSelf
}

// isXMLSpace reports whether c is XML whitespace.
func isXMLSpace(c byte) bool {
	//: space, tab, carriage return, line feed.
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}
