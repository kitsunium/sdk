// Package flatbuffers declares the error codes and the sentinels of the
// FlatBuffers passthrough codec, internal/service/data/codec/flatbuffers —
// range 0.3.23.* (ADR 0006 service/data/codec/flatbuffers block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
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
