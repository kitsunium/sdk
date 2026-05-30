// Package transform — range 0.2.5.* (ADR 0014 core/transform block).
package transform

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.5.0 - 0.2.5.255

// CodeUnknownCompressor identifies a Lookup/Decompress call naming an Algorithm
// that no imported package has registered. It is also surfaced via panic at
// boot on a duplicate registration (see registry.go), not only as a sentinel.
const CodeUnknownCompressor errs.Code = 0x00_02_05_01 // 0.2.5.1

// CodeCompressionFailed identifies a Compress call whose underlying compressor
// returned an error — typically a writer fault while flushing the stream.
const CodeCompressionFailed errs.Code = 0x00_02_05_02 // 0.2.5.2

// CodeDecompressionFailed identifies a Decompress call whose underlying
// decompressor returned an error — malformed input or a truncated stream.
const CodeDecompressionFailed errs.Code = 0x00_02_05_03 // 0.2.5.3

// CodeCompressedFrameInvalid identifies a malformed compressed frame OR a
// tripped decompression-bomb guard (max output size / max expansion ratio). It
// is owned by the pkg/v1/codec frame layer (a later commit); declared here so
// the 0.2.5.* block is allocated in one place per ADR 0014.
const CodeCompressedFrameInvalid errs.Code = 0x00_02_05_04 // 0.2.5.4
