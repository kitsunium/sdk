// Package scheduler — range 0.2.12.* (ADR 0041 core/scheduler block).
package scheduler

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.12.0 - 0.2.12.255

// CodeInvalidEntry identifies an Add refused because the entry could never
// run: an empty name, a nil Job, or a nil Schedule.
const CodeInvalidEntry errs.Code = 0x00_02_0C_01 // 0.2.12.1

// CodeDuplicateJob identifies an Add refused because the entry's name is
// already registered on this Scheduler.
const CodeDuplicateJob errs.Code = 0x00_02_0C_02 // 0.2.12.2

// CodeSchedulerRunning identifies an Add or a second Run refused because the
// Scheduler is already running.
const CodeSchedulerRunning errs.Code = 0x00_02_0C_03 // 0.2.12.3

// CodeJobPanicked identifies a Job that panicked; the scheduler recovered,
// reported it as this entry's result, and kept running.
const CodeJobPanicked errs.Code = 0x00_02_0C_04 // 0.2.12.4
