// Package failover — range 0.3.19.* (ADR 0005 service/logger/middleware/failover block).
package failover

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.19.0 - 0.3.19.255

// CodeFailoverExhausted identifies a Write call where every downstream
// sink in the failover chain returned a non-nil error. The sentinel wraps
// errors.Join of the per-sink failures so callers can inspect each cause.
const CodeFailoverExhausted errs.Code = 0x00_03_13_01 // 0.3.19.1

// CodeFailoverEmpty identifies a New call made with zero downstream sinks.
const CodeFailoverEmpty errs.Code = 0x00_03_13_02 // 0.3.19.2
