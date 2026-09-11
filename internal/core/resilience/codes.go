// Package resilience — range 0.2.8.* (ADR 0026 core/resilience block).
package resilience

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.8.0 - 0.2.8.255

// CodeRetryExhausted identifies a retry policy that used its whole attempt
// budget without a success (the last attempt's error is the wrap cause).
const CodeRetryExhausted errs.Code = 0x00_02_08_01 // 0.2.8.1

// CodeCircuitOpen identifies a call rejected fast because the circuit breaker is
// in the Open state (the downstream is presumed unhealthy).
const CodeCircuitOpen errs.Code = 0x00_02_08_02 // 0.2.8.2

// CodeRateLimited identifies a call rejected (or whose wait was cancelled)
// because the rate limiter had no token available.
const CodeRateLimited errs.Code = 0x00_02_08_03 // 0.2.8.3

// CodeBulkheadFull identifies a call rejected because the bulkhead's concurrency
// slots were all occupied.
const CodeBulkheadFull errs.Code = 0x00_02_08_04 // 0.2.8.4

// CodeTimeoutExceeded identifies an operation that did not complete within the
// timeout policy's deadline.
const CodeTimeoutExceeded errs.Code = 0x00_02_08_05 // 0.2.8.5

// CodePolicyMisconfigured identifies a policy built with a configuration it
// cannot honour, whose every call is refused without running the operation.
const CodePolicyMisconfigured errs.Code = 0x00_02_08_06 // 0.2.8.6

// CodeFallbackFailed identifies a fallback policy whose primary AND secondary
// operations both failed on a live context (both errors travel as fields —
// neither is dropped). A cancellation is reported as ctx.Err() instead.
const CodeFallbackFailed errs.Code = 0x00_02_08_07 // 0.2.8.7
