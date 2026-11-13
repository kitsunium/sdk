package transform

import (
	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
)

// : Asserts at compile time that the zstd and s2 compressors are the bounded port.
var (
	_ coretransform.BoundedDecompressor = (*ZstdCompressor)(nil)
	_ coretransform.BoundedDecompressor = (*S2Compressor)(nil)
)
