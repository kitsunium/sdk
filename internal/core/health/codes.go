// Package health — range 0.2.29.* (ADR 0060 core/health block).
package health

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.29.0 - 0.2.29.255

// CodeInvalidCheck identifies a registration refused because the check could
// never run: an empty name or a nil body.
const CodeInvalidCheck errs.Code = 0x00_02_1D_01 // 0.2.29.1

// CodeDuplicateCheck identifies a registration refused because the name is
// already taken on that probe.
const CodeDuplicateCheck errs.Code = 0x00_02_1D_02 // 0.2.29.2

// CodeUnknownProbe identifies a Probe value this package never mints,
// including the zero value.
const CodeUnknownProbe errs.Code = 0x00_02_1D_03 // 0.2.29.3

// CodeCheckPanicked identifies a check whose body panicked; the engine
// recovered it and turned it into that check's failure.
const CodeCheckPanicked errs.Code = 0x00_02_1D_04 // 0.2.29.4
