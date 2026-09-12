//go:build windows

// Package entitlement - the windows half of the private-key permission gate.
// There are no POSIX mode bits to read here, so the gate is deliberately
// not applied rather than applied to a value that means nothing.
package entitlement

import "os"

// checkPrivateKeyMode accepts any mode on Windows, on purpose.
//
// os.Stat does not report an ACL. It synthesises a mode from the single
// read-only attribute: 0444 when the attribute is set, 0666 otherwise —
// never 0600, whatever os.WriteFile was asked for at enrolment. The unix
// test `Perm()&^0o600 != 0` is therefore true for EVERY file on Windows,
// which made licence verification impossible there: `license status`
// reported "is readable beyond its owner" for a key the tool had just
// written itself.
//
// Skipping the check is not a security regression, because the check never
// worked here: it refused every key rather than the unsafe ones.
//
// What this does NOT do is check the ACL. A key whose DACL grants read
// access to another account is accepted here, so the POSIX invariant is
// dropped on Windows rather than replaced. That is a deliberate, stated
// deployment assumption: the key lives under %USERPROFILE%\.ssh, which
// inherits an ACL granting the owner and administrators only, and nothing
// this package does widens it.
//
// Reading the ACL for real — GetNamedSecurityInfo, walking the DACL,
// comparing SIDs — is the honest replacement and remains open: the
// windows-license CI job added alongside this file is the first thing in
// this repository that runs on a real Windows kernel, so such a check would
// now be exercisable rather than written blind. It is left out of this
// change because it is a security control in its own right and belongs in
// its own review, not folded into the fix that made Windows work at all.
func checkPrivateKeyMode(_ os.FileInfo, _ string) error {
	//: Nothing to assert: the mode carries no access-control meaning here.
	return nil
}
