// Package validation — declares the sentinel *errs.Error values the tag
// compiler emits. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form.
//
// The VIOLATION codes (0.3.47.1 - 0.3.47.5) deliberately have no sentinel
// here: a violation is a ViolationValue, not an error, and minting five
// unused *errs.Error values to mirror them would invite exactly the confusion
// ADR 0046 §Violations are not errors exists to prevent.
package validation

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A refused tag is a permanent
// configuration fault: the same type will be refused identically forever, and
// the fix is a source change, never a retry.
const exitConfig int = 78

var (
	// InvalidRule is returned by Struct when a validate tag cannot be
	// compiled. The fields name the field, the rule and the clause that
	// failed; the message never guesses at what was meant, it names what was
	// refused — an unsupported dialect construct is refused BY NAME so the fix
	// is one word, not an investigation.
	InvalidRule = errs.Define(CodeInvalidRule, "INVALID_RULE",
		"The validate struct tag could not be compiled",
		"service/validation: a validate tag names an unknown rule, a malformed argument, or a rule the field's kind cannot answer; the fields carry the field name, the rule and the clause",
		errs.WithExitCode(exitConfig))

	// UnsupportedTarget is returned by Struct when T is not a struct type.
	// The tag engine walks fields; there is nothing to walk on a scalar, a
	// slice or a pointer, and inventing a meaning for those would be a
	// different feature wearing this one's name.
	UnsupportedTarget = errs.Define(CodeUnsupportedTarget, "UNSUPPORTED_TARGET",
		"Struct tag validation requires a struct type",
		"service/validation: Struct[T] was instantiated with a non-struct T; the field carries the kind that was seen",
		errs.WithExitCode(exitConfig))
)
