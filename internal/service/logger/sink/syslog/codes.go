// Package syslog: codes.go — range 5300-5399 reserved for the syslog Sink.
// Codes are declared at source as typed constants; the errs registry audit
// verifies uniqueness and range membership.
package syslog

// range: 5300-5399

// CodeSyslogAddrEmpty identifies a New call made with an empty address.
const CodeSyslogAddrEmpty int = 5301

// CodeSyslogDialFailed identifies a New call whose net.Dial failed.
const CodeSyslogDialFailed int = 5302

// CodeSyslogWriteFailed identifies a Write call whose underlying net.Conn
// returned a non-nil error; ExitCode defaults to 74 (EX_IOERR) for this case.
const CodeSyslogWriteFailed int = 5303

// CodeSyslogCloseFailed identifies a Close call whose net.Conn.Close returned
// an error; surfaces network teardown failures so callers can react.
const CodeSyslogCloseFailed int = 5304

// CodeSyslogProtoInvalid identifies a New call with an unsupported network.
// Only "udp" and "tcp" are accepted today.
const CodeSyslogProtoInvalid int = 5305
