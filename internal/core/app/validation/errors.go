// Package validation — declares the sentinel *errs.Error outcomes of the
// domain: the port's, and those the struct-tag compiler emits. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// The VIOLATION codes (0.3.47.1 - 0.3.47.5) deliberately have no sentinel: a
// violation is a ViolationValue, not an error, and minting five unused
// *errs.Error values to mirror them would invite exactly the confusion ADR 0046
// §Violations are not errors exists to prevent.
package validation

import "github.com/kitsunium/sdk/internal/kernel/errs"

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

var (
	// ValidationFailed is what ReportValue.Err returns for a non-empty report.
	// Its fields carry the violation count, the first rule, and the list of
	// paths — never a message and never a value. The full report stays the
	// ReportValue; this error is the shape an error-typed contract can hold.
	ValidationFailed = errs.Define(CodeValidationFailed, "VALIDATION_FAILED",
		"One or more values did not satisfy their constraints",
		"core/app/validation: a ReportValue was converted to an error; the fields carry the violation count and the offending paths",
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
		"core/app/validation: a constraint constructor received bounds or arguments it cannot honour; the fields name the rule and the clause that failed",
		errs.WithExitCode(exitConfig))

	// The struct-tag compiler's refusals, raised by internal/service/app/validation.
	// They are declared here, with the port's, so that the domain's codes and
	// sentinels are in one place (ADR 0160).

	// InvalidRule is returned by Struct when a validate tag cannot be
	// compiled. The fields name the field, the rule and the clause that
	// failed; the message never guesses at what was meant, it names what was
	// refused — an unsupported dialect construct is refused BY NAME so the fix
	// is one word, not an investigation.
	InvalidRule = errs.Define(CodeInvalidRule, "INVALID_RULE",
		"The validate struct tag could not be compiled",
		"service/app/validation: a validate tag names an unknown rule, a malformed argument, or a rule the field's kind cannot answer; the fields carry the field name, the rule and the clause",
		errs.WithExitCode(exitConfig))

	// UnsupportedTarget is returned by Struct when T is not a struct type.
	// The tag engine walks fields; there is nothing to walk on a scalar, a
	// slice or a pointer, and inventing a meaning for those would be a
	// different feature wearing this one's name.
	UnsupportedTarget = errs.Define(CodeUnsupportedTarget, "UNSUPPORTED_TARGET",
		"Struct tag validation requires a struct type",
		"service/app/validation: Struct[T] was instantiated with a non-struct T; the field carries the kind that was seen",
		errs.WithExitCode(exitConfig))
)
