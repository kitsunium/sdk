// Package entitlement — the one error-code range this package owns.
//
// Everything it says about VERIFICATION is said in internal/core/entitlement's
// vocabulary (0.2.35.*): a missing key, a mismatched fingerprint and an
// unproven possession are the contract's situations, and an ssh implementation
// reporting them in its own dialect would make every caller learn a second set
// for the same three answers.
//
// ENROLMENT is not in that contract. Minting a pair is something this package
// does and the port does not describe, so its failures have nowhere to borrow a
// code from, and they carried none at all until this range existed.
//
// Hand-registered in codeRangeOwners (ADR 0035) and already listed in
// //:audit_sources.
package entitlement

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.65.0 - 0.3.65.255

// CodeEnrolmentFailed identifies a key pair that could not be minted.
//
// It covers every step of GenerateKeyPair after the subject has been
// validated: creating the key directory, generating the pair, serialising
// either half, and writing either half. The fields name which.
//
// It is deliberately NOT CodeNoLicence. That one says "this machine is not
// enrolled", whose remedy is to enrol; this one says the attempt to enrol just
// failed, whose remedy is whatever the filesystem reported. Reporting the
// second as the first sends an operator to run the command that has already
// failed.
const CodeEnrolmentFailed errs.Code = 0x00_03_41_01 // 0.3.65.1
