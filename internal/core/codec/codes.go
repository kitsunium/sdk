// Package codec: codes.go — range 2200-2299 reserved for core/codec.
package codec

// range: 2200-2299

// CodeDuplicateRegistration identifies an init-time collision on the codec
// registry; two codecs tried to claim the same Name.
const CodeDuplicateRegistration int = 2201

// CodeEmptyInput identifies Unmarshal being called with a zero-byte slice.
const CodeEmptyInput int = 2202

// CodeTargetInvalid identifies Unmarshal being called with a target that is
// not a non-nil pointer to a storable value.
const CodeTargetInvalid int = 2203

// CodeValueInvalid identifies Marshal being called on a value the codec
// cannot serialise (channel, function, unsupported map key).
const CodeValueInvalid int = 2204
