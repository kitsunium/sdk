// Package health — range 0.3.59.* (ADR 0060 service/health block).
package health

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.59.0 - 0.3.59.255

// CodeCheckFailed identifies a check that returned an error. It is the code
// the registry wraps a PLAIN cause with; an *errs.Error cause keeps its own
// code, reason and Public through origin-wins, which is how a caller's own
// wire-safe message reaches a probe body and a driver's does not.
const CodeCheckFailed errs.Code = 0x00_03_3B_01 // 0.3.59.1

// CodeCheckTimeout identifies a check that had not answered when its budget
// expired. Its context was cancelled and the registry stopped waiting; the
// goroutine was not killed and nothing it holds was closed on its behalf.
const CodeCheckTimeout errs.Code = 0x00_03_3B_02 // 0.3.59.2

// CodeStaleCacheWindow identifies a readiness registration refused because its
// MaxAge exceeds [MaxCacheAge].
const CodeStaleCacheWindow errs.Code = 0x00_03_3B_03 // 0.3.59.3

// CodeStartupPending identifies a readiness probe answered while the process
// is still starting. No readiness check was run.
const CodeStartupPending errs.Code = 0x00_03_3B_04 // 0.3.59.4

// CodeDraining identifies a readiness probe answered after Drain. No readiness
// check was run, and no later probe will report anything else.
const CodeDraining errs.Code = 0x00_03_3B_05 // 0.3.59.5

// CodeNotifyFailed identifies an opt-in sd_notify datagram that could not be
// delivered.
const CodeNotifyFailed errs.Code = 0x00_03_3B_06 // 0.3.59.6
