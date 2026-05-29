// Package writer — range 0.2.3.* (ADR 0012 core/writer block).
package writer

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.3.0 - 0.2.3.255

// CodeDuplicateRegistration identifies an init-time collision on the writer
// registry; two distinct factories tried to claim the same Name.
const CodeDuplicateRegistration errs.Code = 0x00_02_03_01 // 0.2.3.1

// CodeWriterUnknownName identifies an Open call for a Name that no imported
// package has registered.
const CodeWriterUnknownName errs.Code = 0x00_02_03_02 // 0.2.3.2

// CodeWriterConfigInvalid identifies a factory receiving a Config whose
// concrete type does not match what it expects.
const CodeWriterConfigInvalid errs.Code = 0x00_02_03_03 // 0.2.3.3

// CodeWriterNil identifies a Register call made with a nil Factory.
const CodeWriterNil errs.Code = 0x00_02_03_04 // 0.2.3.4
