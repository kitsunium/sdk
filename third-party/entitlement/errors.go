// Package entitlement — the sentinel this package emits on its own account.
//
// Its Public names no path and no subject. A key directory is a location on
// somebody's disk and a subject is an identifier the vendor's roster keys on;
// both belong in Fields, which enrolFailed puts them in and which
// errs.FieldsOf reads back.
package entitlement

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitCantCreate matches sysexits EX_CANTCREAT (73). Every failure under
// EnrolmentFailed is an output file that could not be created or written,
// which is exactly what that status is for.
const exitCantCreate int = 73

// EnrolmentFailed is returned when GenerateKeyPair cannot mint the pair.
//
// Nothing partial is left behind that a later run cannot overwrite, but
// the private half IS written before the public one, so a failure between
// the two leaves a key with no published half — which the next
// DiscoverSubject reads as an unusable identity rather than as an
// enrolled one, and which re-running enrolment replaces.
var EnrolmentFailed = errs.Define(CodeEnrolmentFailed, "ENROLMENT_FAILED",
	"the entitlement key pair could not be created",
	"third-party/entitlement: a step of GenerateKeyPair failed; the fields name the step, the key directory and the subject",
	errs.WithExitCode(exitCantCreate))
