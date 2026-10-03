// Package secret — ranges 0.2.37.* (the port's verdicts) and 0.3.68.* (the
// engines' own refusals) — ADR 0096 and ADR 0142, declared here since ADR 0160.
package secret

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.37.0 - 0.2.37.255

// CodeNotFound identifies a secret name that holds no version in the store
// asked: never written, or supplied by nobody.
const CodeNotFound errs.Code = 0x00_02_25_01 // 0.2.37.1

// CodeInvalidName identifies a name outside the secret-name grammar: empty,
// longer than MaxNameLen, or carrying a character outside the closed alphabet.
const CodeInvalidName errs.Code = 0x00_02_25_02 // 0.2.37.2

// CodeReadOnly identifies a write — Put or Prune — refused by a store that
// only reads, such as the environment.
const CodeReadOnly errs.Code = 0x00_02_25_03 // 0.2.37.3

// CodeStoreUnavailable identifies a backend that could not be read or written:
// a permission error, a full disk, a vanished directory, a lock that could not
// be taken.
const CodeStoreUnavailable errs.Code = 0x00_02_25_04 // 0.2.37.4

// CodeValueRefused identifies a secret that could not be decoded: anything but
// a string, or the redaction placeholder itself fed back as if it were a value.
const CodeValueRefused errs.Code = 0x00_02_25_05 // 0.2.37.5

// CodeEmptyValue identifies a Put of an empty secret. An empty secret is what
// an unfilled field looks like, so it is refused rather than stored.
const CodeEmptyValue errs.Code = 0x00_02_25_06 // 0.2.37.6

// CodeInvalidKeep identifies a Prune asked to keep fewer than one version.
// Keeping none would delete the secret, which is not what pruning means.
const CodeInvalidKeep errs.Code = 0x00_02_25_07 // 0.2.37.7

// CodeInvalidSubject identifies a subject outside the subject grammar: empty,
// longer than MaxSubjectLen, or carrying a character outside the closed
// alphabet (ADR 0142).
const CodeInvalidSubject errs.Code = 0x00_02_25_08 // 0.2.37.8

// range: 0.3.68.0 - 0.3.68.255
//
// The refusals of the engines in internal/service/security/secret — the
// concrete stores, the keyring, the rotator, the key file and the subject
// keys. The range was allocated in the service layer (LL = 3) and is declared
// here, beside the domain's verdicts, since ADR 0160: LL records the layer that
// allocated a range, not the directory its declaration lives in, so the values
// never change.

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

// CodeKeyDestroyed identifies a box whose subject data key is not held:
// destroyed by an erasure, or never made (ADR 0142).
const CodeKeyDestroyed errs.Code = 0x00_03_44_09 // 0.3.68.9

// CodeSubjectKeyUnreadable identifies a subject data key that is held and does
// not unwrap under the root keyring: the version that wrapped it is no longer
// kept, the root secret was replaced, or the stored bytes were altered (ADR
// 0142).
const CodeSubjectKeyUnreadable errs.Code = 0x00_03_44_0A // 0.3.68.10
