// Package id — range 0.2.7.* (ADR 0024 core/id block).
package id

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.7.0 - 0.2.7.255

// CodeUnknownScheme identifies a New/Lookup call naming a Scheme that no
// imported package has registered (a missing blank-import of the scheme).
const CodeUnknownScheme errs.Code = 0x00_02_07_01 // 0.2.7.1

// CodeDuplicateRegistration identifies a boot-time collision on the Generator
// registry: a nil scheme, or a distinct generator claiming an already-registered
// Scheme. Surfaced via panic at boot (see registry.go). Reason
// DUPLICATE_REGISTRATION keeps the bracket header a valid (code,reason) pairing
// per ADR 0005, mirroring codec/transform (0.2.2.1 / 0.2.5.5).
const CodeDuplicateRegistration errs.Code = 0x00_02_07_05 // 0.2.7.5
