// Package validation — declares the sentinel *errs.Error port outcomes. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package validation

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A constraint refused at
// construction is a permanent configuration fault: the same arguments will be
// refused forever, and the fix is a code change at the call site, never a retry.
const exitConfig int = 78

// httpUnprocessable is RFC 9110 422 — the request was syntactically fine and
// semantically wrong, which is exactly what a failed validation is. The errs
// default (500) would tell an HTTP edge that the SERVER is at fault.
const httpUnprocessable int = 422

var (
	// ValidationFailed is what ReportValue.Err returns for a non-empty report.
	// Its fields carry the violation count, the first rule, and the list of
	// paths — never a message and never a value. The full report stays the
	// ReportValue; this error is the shape an error-typed contract can hold.
	ValidationFailed = errs.Define(CodeValidationFailed, "VALIDATION_FAILED",
		"One or more values did not satisfy their constraints",
		"core/validation: a ReportValue was converted to an error; the fields carry the violation count and the offending paths",
		errs.WithHTTPStatus(httpUnprocessable))

	// ConstraintMisconfigured is returned by a constraint CONSTRUCTOR whose
	// arguments it cannot honour: a minimum above its maximum, a negative
	// length, an empty allowed set, an uncompilable pattern.
	//
	// It is the ADR 0031 half that must not be confused with the other: a
	// validator with NO constraint is legitimate and passes, while a
	// constraint that could never be satisfied — or never be evaluated — is
	// refused before it can quietly reject, or quietly accept, everything.
	ConstraintMisconfigured = errs.Define(CodeConstraintMisconfigured, "CONSTRAINT_MISCONFIGURED",
		"The constraint cannot be built from the given configuration",
		"core/validation: a constraint constructor received bounds or arguments it cannot honour; the fields name the rule and the clause that failed",
		errs.WithExitCode(exitConfig))
)
