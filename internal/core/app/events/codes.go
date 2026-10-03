// Package events — ranges 0.2.22.* (ADR 0053 core/app/events block) and
// 0.3.52.* (ADR 0053 service/app/events block, declared here since ADR 0160).
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

// range: 0.3.52.0 - 0.3.52.255
//
// The bus's verdicts on a dispatch. The range was allocated to
// internal/service/app/events, which raises these codes while it publishes,
// and it is declared here with the registration refusals so that every code
// of the domain is in one place (ADR 0160). A code keeps the value its
// allocation gave it whichever layer declares it, so the layer byte still
// reads 3.

// CodeListenerFailed identifies the bus's own verdict on a listener that
// returned an error. It travels ALONGSIDE that error, never around it.
const CodeListenerFailed errs.Code = 0x00_03_34_01 // 0.3.52.1

// CodeHaltNotPermitted identifies a listener that returned the Halt control
// sentinel without having declared MayHalt at registration. The dispatch was
// NOT stopped and the refusal was collected.
const CodeHaltNotPermitted errs.Code = 0x00_03_34_02 // 0.3.52.2
