// Package crypto — range 0.2.4.* (ADR 0013 core/crypto block).
package crypto

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.4.0 - 0.2.4.255

// CodeDuplicateRegistration identifies an init-time collision on the AEAD
// registry; two distinct schemes tried to claim the same Algorithm or wire id.
// Surfaced via panic at boot (see registry.go), not as an *Error sentinel.
const CodeDuplicateRegistration errs.Code = 0x00_02_04_01 // 0.2.4.1

// CodeUnknownAlgorithm identifies a Seal/SealAs call naming an Algorithm that no
// imported package has registered.
const CodeUnknownAlgorithm errs.Code = 0x00_02_04_02 // 0.2.4.2

// CodeInvalidKey identifies a NewKey call whose input was not exactly KeyLen
// bytes, or a cipher that rejected the key material.
const CodeInvalidKey errs.Code = 0x00_02_04_03 // 0.2.4.3

// CodeDecryptionFailed identifies any Open failure — bad authentication tag,
// malformed framing, unknown wire id, or wrong key. It is deliberately
// undifferentiated so Open cannot become a padding/format oracle.
const CodeDecryptionFailed errs.Code = 0x00_02_04_04 // 0.2.4.4

// CodeEntropyFailed identifies a crypto/rand failure while generating a nonce in
// Seal — a host entropy fault, not a caller error.
const CodeEntropyFailed errs.Code = 0x00_02_04_05 // 0.2.4.5

// CodeUnknownHashAlgorithm identifies a Sum/SumHex call naming a hash Algorithm
// that no imported package has registered.
const CodeUnknownHashAlgorithm errs.Code = 0x00_02_04_06 // 0.2.4.6

// CodeUnknownSignatureAlgorithm identifies a Sign/Verify/GenerateKey call naming
// an Algorithm that no imported signature package has registered.
const CodeUnknownSignatureAlgorithm errs.Code = 0x00_02_04_07 // 0.2.4.7

// CodeSigningFailed identifies a Sign call whose private key was not well-formed
// for the scheme (e.g. the wrong length) — a caller data error, not a fault.
const CodeSigningFailed errs.Code = 0x00_02_04_08 // 0.2.4.8

// CodeKeyGenerationFailed identifies a crypto/rand failure while generating a
// signing keypair in GenerateKey — a host entropy fault, not a caller error.
const CodeKeyGenerationFailed errs.Code = 0x00_02_04_09 // 0.2.4.9

// CodeUnknownKDFAlgorithm identifies a Subkey call naming a key-derivation
// Algorithm that no imported package has registered.
const CodeUnknownKDFAlgorithm errs.Code = 0x00_02_04_0A // 0.2.4.10

// CodeDerivationFailed identifies a Subkey call whose requested length exceeds
// the scheme's maximum output — a caller data error, not a fault.
const CodeDerivationFailed errs.Code = 0x00_02_04_0B // 0.2.4.11

// CodeUnknownPasswordAlgorithm identifies a HashPassword/VerifyPassword call
// whose Algorithm (or PHC id segment) no imported package has registered.
const CodeUnknownPasswordAlgorithm errs.Code = 0x00_02_04_0C // 0.2.4.12

// CodePasswordHashFailed identifies a crypto/rand failure while generating a
// password salt in HashPassword — a host entropy fault, not a caller error.
const CodePasswordHashFailed errs.Code = 0x00_02_04_0D // 0.2.4.13

// CodeInvalidPasswordHash identifies a stored PHC string that cannot be parsed
// during VerifyPassword — server-side data corruption, not a password mismatch.
const CodeInvalidPasswordHash errs.Code = 0x00_02_04_0E // 0.2.4.14
