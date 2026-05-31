// Package transform — range 0.3.26.* (0x1a) (ADR 0014 service/transform block).
package transform

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.26.0 - 0.3.26.255

// CodeGzipFailed identifies a failure inside compress/gzip during either a
// Compress (writer) or Decompress (reader) operation; the wrap trail and the
// core/transform sentinel distinguish the direction.
const CodeGzipFailed errs.Code = 0x00_03_1A_01 // 0.3.26.1

// CodeFlateFailed identifies a failure inside compress/flate during either a
// Compress (writer) or Decompress (reader) operation.
const CodeFlateFailed errs.Code = 0x00_03_1A_02 // 0.3.26.2
