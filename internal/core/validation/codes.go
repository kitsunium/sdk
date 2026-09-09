// Package validation — range 0.2.15.* (ADR 0046 core/validation block).
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
