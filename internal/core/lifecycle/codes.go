// Package lifecycle — range 0.2.19.* (ADR 0050 core/lifecycle block).
package lifecycle

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.19.0 - 0.2.19.255

// CodeInvalidComponent identifies an Add refused because the component could
// never run: an empty name, a nil Start, or a nil Stop.
const CodeInvalidComponent errs.Code = 0x00_02_13_01 // 0.2.19.1

// CodeDuplicateComponent identifies an Add refused because the component's
// name is already registered on this Lifecycle.
const CodeDuplicateComponent errs.Code = 0x00_02_13_02 // 0.2.19.2

// CodeLifecycleRunning identifies an Add or a second Start refused because
// the Lifecycle is already started.
const CodeLifecycleRunning errs.Code = 0x00_02_13_03 // 0.2.19.3

// CodeComponentPanicked identifies a component whose Start or Stop panicked;
// the Lifecycle recovered it and turned it into this component's failure.
const CodeComponentPanicked errs.Code = 0x00_02_13_04 // 0.2.19.4
