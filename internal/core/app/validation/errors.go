// Package validation — ranges 0.2.15.* (ADR 0046 core/app/validation block)
// and 0.3.47.* (ADR 0046 service/app/validation block, declared here since
// ADR 0160).
//
// Package validation — declares the sentinel *errs.Error outcomes of the
// domain: the port's, and those the struct-tag compiler emits. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// The VIOLATION codes (0.3.47.1 - 0.3.47.5) deliberately have no sentinel: a
// violation is a ViolationValue, not an error, and minting five unused
// *errs.Error values to mirror them would invite exactly the confusion ADR 0046
// §Violations are not errors exists to prevent.
package validation

// exitConfig matches sysexits EX_CONFIG (78). A constraint refused at
// construction is a permanent configuration fault: the same arguments will be
// refused forever, and the fix is a code change at the call site, never a retry.
// A refused tag is one too: the same type will be refused identically
// forever, and the fix is a source change.
const exitConfig int = 78

// httpUnprocessable is RFC 9110 422 — the request was syntactically fine and
// semantically wrong, which is exactly what a failed validation is. The errs
// default (500) would tell an HTTP edge that the SERVER is at fault.
const httpUnprocessable int = 422
