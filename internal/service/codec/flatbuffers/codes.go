// Package flatbuffers — range 0.3.23.* (ADR 0006 service/codec/flatbuffers block).
package flatbuffers

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.23.0 - 0.3.23.255

// CodeFlatbuffersBadType identifies a Marshal call whose value is neither
// []byte nor an implementation of BytesProvider.
const CodeFlatbuffersBadType errs.Code = 0x00_03_17_01 // 0.3.23.1

// CodeFlatbuffersBadTarget identifies an Unmarshal call whose target is
// neither *[]byte nor an implementation of BytesAcceptor.
const CodeFlatbuffersBadTarget errs.Code = 0x00_03_17_02 // 0.3.23.2

// CodeFlatbuffersTruncated identifies a buffer shorter than the 4-byte
// root-offset header required by the FlatBuffers wire format, or larger
// than the package-level CWE-400 ceiling.
const CodeFlatbuffersTruncated errs.Code = 0x00_03_17_03 // 0.3.23.3
