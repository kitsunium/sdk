// Package multi — declares the sentinels returned by the fanout
// Sink. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
package multi

import "github.com/kitsunium/sdk/internal/kernel/errs"

// FanoutWriteFailed wraps an errors.Join of the per-sink failures captured
// during a fan-out Write. The wrapped cause exposes each individual sink's
// error chain through errors.Unwrap, so consumers can inspect every fanout
// branch via errors.Is.
var FanoutWriteFailed = errs.Define(CodeFanoutWriteFailed, "FANOUT_WRITE_FAILED",
	"One or more fan-out sinks failed",
	"service/logger/middleware/multi.Write aggregated per-sink errors via errors.Join")
