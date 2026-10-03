// Package nettransport — range 0.3.30.* (ADR 0015 service slot 0x1e).
package nettransport

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.30.0 - 0.3.30.255

// CodeNetTransportDialFailed identifies a failure to establish the transport at
// Open time: a net.Dial (tcp/udp) error or an unparseable/empty HTTP endpoint.
// ExitCode defaults to 74 (EX_IOERR).
const CodeNetTransportDialFailed errs.Code = 0x00_03_1E_01 // 0.3.30.1

// CodeNetTransportWriteFailed identifies a per-record send failure: the
// underlying net.Conn.Write returned an error, or the HTTP POST did not yield a
// 2xx status. ExitCode defaults to 74 (EX_IOERR).
const CodeNetTransportWriteFailed errs.Code = 0x00_03_1E_02 // 0.3.30.2
