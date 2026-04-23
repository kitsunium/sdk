// Package syslog: codes.go — range 0.3.15.* (ADR 0005 service/logger/sink/syslog block).
package syslog

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.15.0 - 0.3.15.255

// CodeSyslogAddrEmpty identifies a New call made with an empty address.
const CodeSyslogAddrEmpty errs.Code = 0x00_03_0F_01 // 0.3.15.1

// CodeSyslogDialFailed identifies a New call whose net.Dial failed.
const CodeSyslogDialFailed errs.Code = 0x00_03_0F_02 // 0.3.15.2

// CodeSyslogWriteFailed identifies a Write call whose underlying net.Conn
// returned a non-nil error; ExitCode defaults to 74 (EX_IOERR) for this case.
const CodeSyslogWriteFailed errs.Code = 0x00_03_0F_03 // 0.3.15.3

// CodeSyslogCloseFailed identifies a Close call whose net.Conn.Close returned
// an error; surfaces network teardown failures so callers can react.
const CodeSyslogCloseFailed errs.Code = 0x00_03_0F_04 // 0.3.15.4

// CodeSyslogProtoInvalid identifies a New call with an unsupported network.
// Only "udp" and "tcp" are accepted today.
const CodeSyslogProtoInvalid errs.Code = 0x00_03_0F_05 // 0.3.15.5

// CodeSyslogCtxCancelled identifies a Write or Flush call whose context was
// already done. Wraps the stdlib context error so the typed-errors-only SDK
// rule is satisfied and consumers can HasCode / errors.Is against it.
const CodeSyslogCtxCancelled errs.Code = 0x00_03_0F_06 // 0.3.15.6
