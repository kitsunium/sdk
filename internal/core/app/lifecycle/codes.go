// Package lifecycle — ranges 0.2.19.* (ADR 0050 core/app/lifecycle block) and
// 0.3.49.* (ADR 0050 service/app/lifecycle block, declared here since ADR 0160).
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

// range: 0.3.49.0 - 0.3.49.255
//
// The engine's and the supervisor's outcomes. The range was allocated to
// internal/service/app/lifecycle, which raises these codes while it starts,
// stops and supervises, and it is declared here with the registration refusals
// so that every code of the domain is in one place (ADR 0160). A code keeps
// the value its allocation gave it whichever layer declares it, so the layer
// byte still reads 3.

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

// CodeRunPanicked identifies a supervised run that panicked; the supervisor
// recovered it, counted it as a failure, and restarts the run after its
// backoff.
const CodeRunPanicked errs.Code = 0x00_03_31_06 // 0.3.49.6

// CodeSupervisorMisconfigured identifies a supervisor refused at
// construction: no name, or no function to run.
const CodeSupervisorMisconfigured errs.Code = 0x00_03_31_07 // 0.3.49.7

// CodeSupervisorRunning identifies a Start refused because the supervisor is
// already supervising.
const CodeSupervisorRunning errs.Code = 0x00_03_31_08 // 0.3.49.8
