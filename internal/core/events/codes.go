// Package events — range 0.2.22.* (ADR 0053 core/events block).
package events

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.22.0 - 0.2.22.255

// CodeInvalidSubscription identifies a Subscribe refused because the
// subscription could never run: an empty name or a nil Listener.
const CodeInvalidSubscription errs.Code = 0x00_02_16_01 // 0.2.22.1

// CodeDuplicateListener identifies a Subscribe refused because a listener is
// already registered under that name for that event type.
const CodeDuplicateListener errs.Code = 0x00_02_16_02 // 0.2.22.2

// CodeUnknownListener identifies an Unsubscribe naming a listener that is not
// registered for that event type.
const CodeUnknownListener errs.Code = 0x00_02_16_03 // 0.2.22.3

// CodeInvalidEventType identifies an event type a dispatch could never match:
// a nil or interface type at Subscribe, or a nil event at Publish.
const CodeInvalidEventType errs.Code = 0x00_02_16_04 // 0.2.22.4

// CodeListenerPanicked identifies a listener whose call panicked; the bus
// recovered it, turned it into this listener's failure, and carried on with
// the remaining listeners.
const CodeListenerPanicked errs.Code = 0x00_02_16_05 // 0.2.22.5

// CodeHalt identifies the control sentinel a listener returns to stop the
// dispatch. It is the one code in this range that never reaches a caller: the
// bus consumes it and reports the halt through DispatchValue instead.
const CodeHalt errs.Code = 0x00_02_16_06 // 0.2.22.6
