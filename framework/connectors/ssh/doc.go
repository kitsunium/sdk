// Package ssh — the one error-code range this package owns.
//
// Everything it says about VERIFICATION is said in the entitlement contract's
// vocabulary — framework/entitlement's sentinels, 0.2.35.*: a missing key, a
// mismatched fingerprint and an unproven possession are the contract's
// situations, and an ssh implementation reporting them in its own dialect would
// make every caller learn a second set for the same three answers.
//
// ENROLMENT is not in that contract. Minting a pair is something this package
// does and the port does not describe, so its failures have nowhere to borrow a
// code from, and they carried none at all until this range existed.
//
// Registered in design/sdk.yaml's codes.ranges, which kit writes into
// codeRangeOwners (ADR 0035, ADR 0164), and already listed in
// //:audit_sources.
//
// Package ssh — discovering which subject this machine is enrolled as,
// from the layout ssh keys actually have on disk.
//
// This is the half of the Identity port that depends on the ssh key FORMAT and
// the ~/.ssh convention, which is why it lives here rather than with the
// mechanism (kitsunium/sdk ADR 0078).
//
// Package ssh - enrolment: minting a subject identity locally and turning
// it into a request the vendor can act on. The private half never leaves the
// machine; what travels is the public half and the UUID naming it.
//
// Package ssh — the sentinel this package emits on its own account.
//
// Its Public names no path and no subject. A key directory is a location on
// somebody's disk and a subject is an identifier the vendor's roster keys on;
// both belong in Fields, which enrolFailed puts them in and which
// errs.FieldsOf reads back.
//
// Package ssh - local key material and the possession proof. The public
// halves are published by design, so reading one proves nothing; only a
// fresh signature over a per-call nonce establishes ownership.
//
// Package ssh - the unix half of the private-key permission gate. The
// mode bits mean what they say here, so they are worth refusing on.
//
// Package ssh - the windows half of the private-key permission gate.
// There are no POSIX mode bits to read here, so the gate is deliberately
// not applied rather than applied to a value that means nothing.
//
// Package ssh is the ssh implementation of the framework's entitlement
// Identity port, plus enrolment: minting a subject identity locally and turning
// it into a request the vendor can act on.
//
// It is a connector module of its own (ADR 0158), the shape the database
// engines have: the mechanism — roster, signature, offline cache, anti-rollback
// ratchet, CI seat, version floor — is the framework's entitlement package,
// which reaches no ssh code, while golang.org/x/crypto/ssh, and the
// golang.org/x/sys it brings, are required by this module and nothing else.
//
//	identity := ssh.NewSSHIdentity("") // the user's ~/.ssh
//	service := entitlement.New(identity, vendorKey, &product)
//	grant, err := service.Verify(time.Now())
//
// Possession is proven against key material the user ALREADY has, and every
// refusal is said in the entitlement contract's vocabulary (0.2.35.*); the one
// failure that contract has no word for, a failed enrolment, is this package's
// own CodeEnrolmentFailed (0.3.65.1).
//
// Package ssh — compile-time proof that the ssh implementation still
// satisfies the port it is written against.
//
// Package ssh — the three shapes every error here is built with, so the
// choice at a call site is which sentinel rather than which spelling.
//
// The split between the first two is whether this package DECIDED the failure
// or was TOLD about one. A decision has no cause to carry — nothing failed, a
// rule was applied to a filename or a mode — and the sentinel itself is the
// whole of it. A report from outside has a cause that must survive, because
// errors.Is(err, fs.ErrNotExist) and the text the operating system wrote are
// the two things a wrapper most often destroys.
//
// They are a copy of framework/internal/service/entitlement's, and deliberately not an
// import: that package is a separate Go module, so its unexported helpers are
// unreachable from here, and exporting them would put three wrapping shapes on
// the surface of a domain whose whole public API is a three-method port and a
// facade. Thirty lines duplicated across a module boundary is the smaller cost,
// and the two copies are pinned to the same behaviour by having the same tests
// on both sides.
package ssh
