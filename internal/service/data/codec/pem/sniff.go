package pem

import "bytes"

// beginHead opens every PEM block's encapsulation boundary (RFC 7468 §2).
var beginHead = []byte("-----BEGIN ")

// Sniff implements codec.Sniffer (#909): prefix is PEM when, after leading
// whitespace, it opens with "-----BEGIN ", the pre-encapsulation boundary.
// It reads only prefix and allocates nothing.
func (*pemCodec) Sniff(prefix []byte) bool {
	//: the boundary, after any blank lines.
	return bytes.HasPrefix(bytes.TrimLeft(prefix, " \t\r\n"), beginHead)
}
