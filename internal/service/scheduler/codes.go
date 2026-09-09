// Package scheduler — range 0.3.43.* (ADR 0041 service/scheduler block).
package scheduler

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.43.0 - 0.3.43.255

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
