// Package multi — range 0.3.16.* (ADR 0005 service/logger/middleware/multi block).
package multi

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.16.0 - 0.3.16.255

// CodeFanoutWriteFailed identifies a Write call where one or more of the
// fan-out targets returned a non-nil error. The error wraps an aggregated
// errors.Join of the per-sink failures so callers can drill in via errors.Is
// against the individual sink sentinels.
const CodeFanoutWriteFailed errs.Code = 0x00_03_10_01 // 0.3.16.1

// FanoutWriteFailed wraps an errors.Join of the per-sink failures captured
// during a fan-out Write. The wrapped cause exposes each individual sink's
// error chain through errors.Unwrap, so consumers can inspect every fanout
// branch via errors.Is.
var FanoutWriteFailed = errs.Define(CodeFanoutWriteFailed, "FANOUT_WRITE_FAILED",
	"One or more fan-out sinks failed",
	"service/logger/middleware/multi.Write aggregated per-sink errors via errors.Join")
