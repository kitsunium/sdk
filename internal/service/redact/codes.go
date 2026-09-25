// Package redact — range 0.3.73.* (ADR 0101 service/redact block).
package redact

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.73.0 - 0.3.73.255

// CodeDocumentInvalid identifies a document handed to JSON that is not one
// well-formed JSON value, so there is nothing to redact into.
const CodeDocumentInvalid errs.Code = 0x00_03_49_01 // 0.3.73.1

// CodeValueUnencodable identifies a value handed to Value that encoding/json
// refuses to encode: a channel, a function, a cycle, a failing Marshaler.
const CodeValueUnencodable errs.Code = 0x00_03_49_02 // 0.3.73.2
