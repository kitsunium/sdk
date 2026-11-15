package transform

import (
	"github.com/klauspost/compress/zstd"
)

// : Asserts at compile time that both schemes' stream decoders, and the
// : bounded stream over them, read and write into a writer (#892).
var (
	_ streamDecoder = (*zstd.Decoder)(nil)
	_ streamDecoder = (*s2Frame)(nil)
	_ streamDecoder = (*boundedStream)(nil)
)
