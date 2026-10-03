// Package validation — ranges 0.2.15.* (ADR 0046 core/app/validation block)
// and 0.3.47.* (ADR 0046 service/app/validation block, declared here since
// ADR 0160).
package validation

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.15.0 - 0.2.15.255

// CodeValidationFailed identifies the aggregate error a non-empty ReportValue
// converts to. It is the code an error-typed contract sees; the individual
// rules keep their own codes inside the report.
const CodeValidationFailed errs.Code = 0x00_02_0F_01 // 0.2.15.1

// CodeConstraintMisconfigured identifies a constraint refused AT CONSTRUCTION
// because its configuration cannot be honoured — an inverted numeric range, a
// negative length bound, an empty allowed set, an uncompilable pattern. It is
// never a validation outcome: the value was never reached.
const CodeConstraintMisconfigured errs.Code = 0x00_02_0F_02 // 0.2.15.2

// range: 0.3.47.0 - 0.3.47.255
//
// The rules' violation codes and the tag compiler's refusals. The range was
// allocated to internal/service/app/validation, whose constraints and tag
// compiler use these codes, and it is declared here with the port's so that
// every code of the domain is in one place (ADR 0160). A code keeps the value
// its allocation gave it whichever layer declares it, so the layer byte still
// reads 3.

// CodeRequired identifies a value that is absent where presence was required.
// It is a VIOLATION code — it travels on a ViolationValue, not on an error.
const CodeRequired errs.Code = 0x00_03_2F_01 // 0.3.47.1

// CodeOutOfRange identifies an ordered value outside its allowed bounds.
const CodeOutOfRange errs.Code = 0x00_03_2F_02 // 0.3.47.2

// CodeLengthOutOfRange identifies a string, slice or array whose SIZE is
// outside its allowed bounds. It is distinct from CodeOutOfRange because
// "between 3 and 10" and "3 to 10 characters long" are different rules with
// different fixes, and a caller routing on the code should not have to guess
// which one fired.
const CodeLengthOutOfRange errs.Code = 0x00_03_2F_03 // 0.3.47.3

// CodeNotInSet identifies a value absent from the constraint's allowed set.
const CodeNotInSet errs.Code = 0x00_03_2F_04 // 0.3.47.4

// CodePatternMismatch identifies a string that did not match its pattern.
const CodePatternMismatch errs.Code = 0x00_03_2F_05 // 0.3.47.5

// CodeInvalidRule identifies a struct tag refused at COMPILE time: an unknown
// rule, a malformed argument, a rule applied to a kind it cannot check, or a
// construct this dialect refuses by name. It is an error, never a violation —
// the value was never reached.
const CodeInvalidRule errs.Code = 0x00_03_2F_06 // 0.3.47.6

// CodeUnsupportedTarget identifies a Struct[T] whose T the tag engine cannot
// walk — anything that is not a struct type.
const CodeUnsupportedTarget errs.Code = 0x00_03_2F_07 // 0.3.47.7
