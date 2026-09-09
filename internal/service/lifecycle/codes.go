// Package lifecycle — range 0.3.49.* (ADR 0050 service/lifecycle block).
package lifecycle

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.49.0 - 0.3.49.255

// CodeStartFailed identifies a Start aborted by one component's failure. The
// components already up have been stopped in reverse order by the time this
// is returned.
const CodeStartFailed errs.Code = 0x00_03_31_01 // 0.3.49.1

// CodeStopFailed identifies a component whose Stop returned an error during
// an ordinary shutdown. The remaining components were still stopped.
const CodeStopFailed errs.Code = 0x00_03_31_02 // 0.3.49.2

// CodeStopTimeout identifies a component whose Stop had not returned when its
// budget expired. Its context was cancelled and the engine moved on; nothing
// was severed and the goroutine was not killed.
const CodeStopTimeout errs.Code = 0x00_03_31_03 // 0.3.49.3

// CodeUnwindFailed identifies a failure DURING the cleanup of a partial
// start. It travels alongside the start failure that triggered the unwind, so
// a broken teardown can never hide the reason startup aborted.
const CodeUnwindFailed errs.Code = 0x00_03_31_04 // 0.3.49.4

// CodeReadinessFailed identifies an opt-in sd_notify readiness or stopping
// datagram that could not be delivered.
const CodeReadinessFailed errs.Code = 0x00_03_31_05 // 0.3.49.5
