// Package scheduler — ranges 0.2.12.* (ADR 0041 core/app/scheduler block) and
// 0.3.43.* (ADR 0041 service/app/scheduler block, declared here since ADR 0160).
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

// range: 0.3.43.0 - 0.3.43.255
//
// The parser's refusals of a schedule. The range was allocated to
// internal/service/app/scheduler, which raises these codes while it parses a
// cron expression or an interval, and it is declared here with the
// registration refusals so that every code of the domain is in one place
// (ADR 0160). A code keeps the value its allocation gave it whichever layer
// declares it, so the layer byte still reads 3.

// CodeInvalidExpression identifies a cron expression this parser understands
// the shape of but cannot accept: a malformed field, a value outside its
// field's range, an inverted range, a non-positive step.
const CodeInvalidExpression errs.Code = 0x00_03_2B_01 // 0.3.43.1

// CodeUnsupportedSyntax identifies a cron construct that exists in some other
// dialect and that this parser refuses BY NAME rather than by guessing: a
// six-field (seconds) expression, @reboot, @every, and the Quartz L / W / # /
// ? operators.
const CodeUnsupportedSyntax errs.Code = 0x00_03_2B_02 // 0.3.43.2

// CodeUnreachableSchedule identifies a syntactically valid expression that
// matches no instant on the calendar — "31 April", "30 February" — and is
// therefore refused at construction rather than registered as a job that would
// never run.
const CodeUnreachableSchedule errs.Code = 0x00_03_2B_03 // 0.3.43.3

// CodeInvalidLocation identifies a nil *time.Location handed to
// ParseInLocation, which is what an unchecked time.LoadLocation looks like.
const CodeInvalidLocation errs.Code = 0x00_03_2B_04 // 0.3.43.4

// CodeInvalidInterval identifies a non-positive duration handed to Every,
// which would be a schedule due infinitely often.
const CodeInvalidInterval errs.Code = 0x00_03_2B_05 // 0.3.43.5
