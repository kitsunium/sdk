// Package redact — range 0.3.73.* (ADR 0101), declared here since ADR 0160.
package redact

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.73.0 - 0.3.73.255
//
// The range was allocated in the service layer (LL = 3), when the engine in
// internal/service/security/redact was the whole domain (ADR 0101 §D5), and is
// declared here since ADR 0160 gave the domain a core: LL records the layer
// that allocated a range, not the directory its declaration lives in, so the
// values never change.

// CodeDocumentInvalid identifies a document handed to JSON that is not one
// well-formed JSON value, so there is nothing to redact into.
const CodeDocumentInvalid errs.Code = 0x00_03_49_01 // 0.3.73.1

// CodeValueUnencodable identifies a value handed to Value that encoding/json
// refuses to encode: a channel, a function, a cycle, a failing Marshaler.
const CodeValueUnencodable errs.Code = 0x00_03_49_02 // 0.3.73.2
