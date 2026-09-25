// Package secret — range 0.2.37.* (ADR 0096 core/secret block).
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
