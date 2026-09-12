// Package entitlement — range 0.3.65.* (third-party/entitlement block).
package entitlement

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.65.0 - 0.3.65.255

// CodeNoLicence identifies a machine with no entitlement key at all.
const CodeNoLicence errs.Code = 0x00_03_41_01 // 0.3.65.1

// CodeRosterUnsigned identifies a roster carrying no valid vendor signature.
// It is the spoofing signal: a substituted endpoint cannot produce one.
const CodeRosterUnsigned errs.Code = 0x00_03_41_02 // 0.3.65.2

// CodeRosterStale identifies a roster that verified but whose validity window
// has closed.
const CodeRosterStale errs.Code = 0x00_03_41_03 // 0.3.65.3

// CodeRevoked identifies a subject absent from a roster that was itself valid.
const CodeRevoked errs.Code = 0x00_03_41_04 // 0.3.65.4

// CodeLicenceExpired identifies a subject whose own validity window has closed.
const CodeLicenceExpired errs.Code = 0x00_03_41_05 // 0.3.65.5

// CodeKeyMismatch identifies a local key that does not match the fingerprint
// the roster publishes for this subject.
const CodeKeyMismatch errs.Code = 0x00_03_41_06 // 0.3.65.6

// CodeRosterUnreachable identifies a fetch that could not decide either way.
// It reports "cannot decide", never "no" — the distinction is the whole point.
const CodeRosterUnreachable errs.Code = 0x00_03_41_07 // 0.3.65.7

// CodeAmbiguousLicence identifies more than one entitlement identity on one
// machine, which no automatic choice can resolve safely.
const CodeAmbiguousLicence errs.Code = 0x00_03_41_08 // 0.3.65.8

// CodeCIUnverifiable identifies a CI run whose provenance could not be proven.
const CodeCIUnverifiable errs.Code = 0x00_03_41_09 // 0.3.65.9

// CodeCIUnknownKey identifies a CI token signed by a key the issuer does not
// publish.
const CodeCIUnknownKey errs.Code = 0x00_03_41_0A // 0.3.65.10

// CodeCINotEntitled identifies a CI account the roster does not cover.
const CodeCINotEntitled errs.Code = 0x00_03_41_0B // 0.3.65.11

// CodeNoPossession identifies a holder who could not prove possession of the
// private half of the published key.
const CodeNoPossession errs.Code = 0x00_03_41_0C // 0.3.65.12

// CodeClockRegressed identifies a machine whose clock is behind the last signed
// document it verified — the anti-rollback ratchet.
const CodeClockRegressed errs.Code = 0x00_03_41_0D // 0.3.65.13

// CodeUpdateRequired identifies a build below the floor the roster mandates.
const CodeUpdateRequired errs.Code = 0x00_03_41_0E // 0.3.65.14

// CodeProductInvalid identifies a ProductValue whose origin list could not
// survive the day it is needed — see ProductValue.Validate.
const CodeProductInvalid errs.Code = 0x00_03_41_0F // 0.3.65.15
