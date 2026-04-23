// Package codec: codes.go — range 0.2.2.* (ADR 0005 core/codec block).
package codec

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.2.0 - 0.2.2.255

// CodeDuplicateRegistration identifies an init-time collision on the codec
// registry; two codecs tried to claim the same Name.
const CodeDuplicateRegistration errs.Code = 0x00_02_02_01 // 0.2.2.1

// CodeEmptyInput identifies Unmarshal being called with a zero-byte slice.
const CodeEmptyInput errs.Code = 0x00_02_02_02 // 0.2.2.2

// CodeTargetInvalid identifies Unmarshal being called with a target that is
// not a non-nil pointer to a storable value.
const CodeTargetInvalid errs.Code = 0x00_02_02_03 // 0.2.2.3

// CodeValueInvalid identifies Marshal being called on a value the codec
// cannot serialise (channel, function, unsupported map key).
const CodeValueInvalid errs.Code = 0x00_02_02_04 // 0.2.2.4
