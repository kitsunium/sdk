// Package redis — range 0.3.34.* (ADR 0015 service slot 0x22).
package redis

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.34.0 - 0.3.34.255

// CodeRedisClientInitFailed identifies a failure to build the Redis client
// at Open time (missing socket/stream or an unresolvable credential provider).
// ExitCode defaults to 74 (EX_IOERR).
const CodeRedisClientInitFailed errs.Code = 0x00_03_22_01 // 0.3.34.1

// CodeRedisXAddFailed identifies a failed pipelined XADD batch on the
// drainer goroutine. ExitCode defaults to 74 (EX_IOERR).
const CodeRedisXAddFailed errs.Code = 0x00_03_22_02 // 0.3.34.2
