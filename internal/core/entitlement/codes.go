// Package entitlement — the error-code range this domain owns (third-party/entitlement block).
package entitlement

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.35.0 - 0.2.35.255

// CodeNoLicence identifies a machine with no entitlement key at all.
const CodeNoLicence errs.Code = 0x00_02_23_01 // 0.2.35.1

// CodeRosterUnsigned identifies a roster carrying no valid vendor signature.
// It is the spoofing signal: a substituted endpoint cannot produce one.
const CodeRosterUnsigned errs.Code = 0x00_02_23_02 // 0.2.35.2

// CodeRosterStale identifies a roster that verified but whose validity window
// has closed.
const CodeRosterStale errs.Code = 0x00_02_23_03 // 0.2.35.3

// CodeRevoked identifies a subject absent from a roster that was itself valid.
const CodeRevoked errs.Code = 0x00_02_23_04 // 0.2.35.4

// CodeLicenceExpired identifies a subject whose own validity window has closed.
const CodeLicenceExpired errs.Code = 0x00_02_23_05 // 0.2.35.5

// CodeKeyMismatch identifies a local key that does not match the fingerprint
// the roster publishes for this subject.
const CodeKeyMismatch errs.Code = 0x00_02_23_06 // 0.2.35.6

// CodeRosterUnreachable identifies a fetch that could not decide either way.
// It reports "cannot decide", never "no" — the distinction is the whole point.
const CodeRosterUnreachable errs.Code = 0x00_02_23_07 // 0.2.35.7

// CodeAmbiguousLicence identifies more than one entitlement identity on one
// machine, which no automatic choice can resolve safely.
const CodeAmbiguousLicence errs.Code = 0x00_02_23_08 // 0.2.35.8

// CodeCIUnverifiable identifies a CI run whose provenance could not be proven.
const CodeCIUnverifiable errs.Code = 0x00_02_23_09 // 0.2.35.9

// CodeCIUnknownKey identifies a CI token signed by a key the issuer does not
// publish.
const CodeCIUnknownKey errs.Code = 0x00_02_23_0A // 0.2.35.10

// CodeCINotEntitled identifies a CI account the roster does not cover.
const CodeCINotEntitled errs.Code = 0x00_02_23_0B // 0.2.35.11

// CodeNoPossession identifies a holder who could not prove possession of the
// private half of the published key.
const CodeNoPossession errs.Code = 0x00_02_23_0C // 0.2.35.12

// CodeClockRegressed identifies a machine whose clock is behind the last signed
// document it verified — the anti-rollback ratchet.
const CodeClockRegressed errs.Code = 0x00_02_23_0D // 0.2.35.13

// CodeUpdateRequired identifies a build below the floor the roster mandates.
const CodeUpdateRequired errs.Code = 0x00_02_23_0E // 0.2.35.14

// CodeProductInvalid identifies a ProductValue whose origin list could not
// survive the day it is needed — see ProductValue.Validate.
const CodeProductInvalid errs.Code = 0x00_02_23_0F // 0.2.35.15
