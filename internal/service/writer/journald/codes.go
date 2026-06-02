// Package journald — range 0.3.31.* (ADR 0015 service slot 0x1f).
package journald

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.31.0 - 0.3.31.255

// CodeJournaldOpenFailed identifies a failure to connect the unix-datagram
// socket at Open time (the journald socket is absent or not connectable).
// ExitCode defaults to 74 (EX_IOERR).
const CodeJournaldOpenFailed errs.Code = 0x00_03_1F_01 // 0.3.31.1

// CodeJournaldWriteFailed identifies a datagram send failure on the journald
// socket. ExitCode defaults to 74 (EX_IOERR).
const CodeJournaldWriteFailed errs.Code = 0x00_03_1F_02 // 0.3.31.2
