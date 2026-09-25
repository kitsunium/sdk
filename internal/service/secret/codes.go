// Package secret — range 0.3.68.* (ADR 0096 service/secret block).
package secret

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.68.0 - 0.3.68.255

// CodeInvalidConfig identifies a store, keyring or rotator that cannot be
// built as configured: a missing directory, a directory other users can read,
// a malformed environment prefix, a rotation policy with no interval.
const CodeInvalidConfig errs.Code = 0x00_03_44_01 // 0.3.68.1

// CodeRecordUnreadable identifies a file-store record that exists and cannot
// be read back: truncated, tampered, written under another key, or written by
// a format this build does not know.
const CodeRecordUnreadable errs.Code = 0x00_03_44_02 // 0.3.68.2

// CodeEnvRefused identifies an environment that names a secret without
// supplying a usable value: both the variable and its _FILE form set, or a
// _FILE naming an empty or oversized file.
const CodeEnvRefused errs.Code = 0x00_03_44_03 // 0.3.68.3

// CodeSealInvalid identifies a box the keyring could not open — the single
// verdict for every cause, so Open is not an oracle.
const CodeSealInvalid errs.Code = 0x00_03_44_04 // 0.3.68.4

// CodeSignatureInvalid identifies a signature the keyring did not verify — the
// single verdict for every cause, so Verify is not an oracle.
const CodeSignatureInvalid errs.Code = 0x00_03_44_05 // 0.3.68.5

// CodeKeyMaterialInvalid identifies a secret version a keyring cannot use as a
// key because it is not exactly one crypto.Key long.
const CodeKeyMaterialInvalid errs.Code = 0x00_03_44_06 // 0.3.68.6

// CodeGenerateFailed identifies a rotation whose policy could not produce a
// new secret: the generator returned an error or an empty value.
const CodeGenerateFailed errs.Code = 0x00_03_44_07 // 0.3.68.7

// CodeKeyFileInvalid identifies a key file that exists and does not hold one
// key: not a regular file, or not exactly one crypto.Key long.
const CodeKeyFileInvalid errs.Code = 0x00_03_44_08 // 0.3.68.8
