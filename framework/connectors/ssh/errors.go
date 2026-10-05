// Package ssh — the sentinel this package emits on its own account.
//
// Its Public names no path and no subject. A key directory is a location on
// somebody's disk and a subject is an identifier the vendor's roster keys on;
// both belong in Fields, which enrolFailed puts them in and which
// errs.FieldsOf reads back.
package ssh

// exitCantCreate matches sysexits EX_CANTCREAT (73). Every failure under
// EnrolmentFailed is an output file that could not be created or written,
// which is exactly what that status is for.
const exitCantCreate int = 73
