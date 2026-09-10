// Package transform — range 0.3.63.* (ADR 0066 third-party/transform block).
package transform

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.63.0 - 0.3.63.255

// CodeZstdFailed identifies a failure inside the zstd codec during either a
// Compress or a Decompress operation — a malformed frame, a truncated stream,
// or a checksum mismatch. It is deliberately NOT the code a tripped output
// bound produces: see CodeDecompressionLimitExceeded.
const CodeZstdFailed errs.Code = 0x00_03_3F_01 // 0.3.63.1

// CodeS2Failed identifies a failure inside the s2 codec during either a
// Compress or a Decompress operation — a corrupt block header, a length header
// the payload does not honour, or an input larger than the block format can
// address.
const CodeS2Failed errs.Code = 0x00_03_3F_02 // 0.3.63.2

// CodeDecompressionLimitExceeded identifies a decompression refused because its
// output would exceed the compressor's configured ceiling — a decompression
// bomb, or a legitimate payload larger than this compressor was built for.
//
// It is a SEPARATE code from CodeZstdFailed / CodeS2Failed on purpose. "Someone
// sent us bytes we could not parse" and "someone sent us bytes engineered to
// exhaust this process" are different events for whoever reads the logs: the
// first is traffic, the second is an attack signature worth alerting on, and a
// shared code makes the second invisible inside the first.
const CodeDecompressionLimitExceeded errs.Code = 0x00_03_3F_03 // 0.3.63.3

// CodeLimitMisconfigured identifies a constructor call whose
// maxDecompressedBytes argument is not a positive byte count. The value is
// REFUSED rather than defaulted because its two natural readings — "no limit"
// and "refuse everything" — are opposites, and picking either on the caller's
// behalf silently grants or silently denies (ADR 0031 §refuse, ADR 0066 D4).
const CodeLimitMisconfigured errs.Code = 0x00_03_3F_04 // 0.3.63.4
