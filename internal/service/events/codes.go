// Package events — range 0.3.52.* (ADR 0053 service/events block).
package events

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.52.0 - 0.3.52.255

// CodeListenerFailed identifies the bus's own verdict on a listener that
// returned an error. It travels ALONGSIDE that error, never around it.
const CodeListenerFailed errs.Code = 0x00_03_34_01 // 0.3.52.1

// CodeHaltNotPermitted identifies a listener that returned the Halt control
// sentinel without having declared MayHalt at registration. The dispatch was
// NOT stopped and the refusal was collected.
const CodeHaltNotPermitted errs.Code = 0x00_03_34_02 // 0.3.52.2
