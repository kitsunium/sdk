// Package multi declares the codes and sentinels of the logger's fan-out
// middleware, internal/service/observe/logger/middleware/multi: range
// 0.3.16.*, allocated to that engine (ADR 0005 service/observe/logger/middleware/multi
// block) and declared here since ADR 0160, so the engine declares none.
package multi

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.16.0 - 0.3.16.255

// CodeFanoutWriteFailed identifies a Write call where one or more of the
// fan-out targets returned a non-nil error. The error wraps an aggregated
// errors.Join of the per-sink failures so callers can drill in via errors.Is
// against the individual sink sentinels.
const CodeFanoutWriteFailed errs.Code = 0x00_03_10_01 // 0.3.16.1
