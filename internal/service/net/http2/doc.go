// Package http2 is the SDK's HTTP/2 codec for the loop engine's sibling
// profile (ADR 0221, amending ADR 0186 §D6): pure functions over []byte and
// no I/O, the shape http1 has. The frame layer (ParseFrameHeader and the
// Append family) reads and writes RFC 9113's frames; Decoder and Encoder are
// HPACK (RFC 7541), written in the SDK because the standard library
// exports none.
//
// # The frame layer
//
// ParseFrameHeader reads the nine-byte header and judges what those bytes
// alone can: the length against the connection's frame bound and, for a
// padded frame, the padding against the length — it reads the pad length
// byte, so a padded frame's header needs ten bytes. Neither reads more,
// and neither allocates. Whether a frame's stream may carry it is the
// connection state machine's.
//
// # HPACK
//
// Decoder is one connection's decoder. Its table is a value with no pointer
// in it: the bytes of the dynamic table are memory its caller hands to
// Decode, so a connection's state fits a pointer-free slice and an idle
// connection pays nothing until its first field block.
//
// Decode fills a request value's Fields in arrival order, pseudo-fields
// included, never allocating when its scratch has room: a value is aliased
// from the block when it arrived as a plain literal, written into scratch
// when it was Huffman-coded or came from the table. A field the list
// ceiling refuses is refused before it is copied and before any insert, so a
// block that expands through indexed fields costs the ceiling and nothing
// more (RFC 7541 §7.3's bomb).
//
// A block refused on the way (a bad index, a size update out of place, the
// list ceiling, a Huffman string that breaks RFC 7541 §5.2) leaves the
// table undefined: the caller ends the connection with
// GOAWAY(COMPRESSION_ERROR), as RFC 9113 §4.3 demands. A field RFC 9113
// §8.2.1 calls malformed is different: the block is decoded to its end, the
// table is intact, Decode returns FieldMalformed, and only the stream is
// refused.
//
// Encoder writes response blocks: :status and the handler's own field lines
// re-read as never-indexed literals, a name from the static table when it
// has one and no Huffman coding, so the peer's table size needs no honouring
// beyond zero. AppendHeaders splits a block past the peer's frame size over
// HEADERS and CONTINUATION frames.
package http2
