// Package syslog: errors.go declares the sentinels returned by the syslog
// Sink. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
package syslog

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitIOErr matches sysexits EX_IOERR — used by WriteFailed to let CLI
// consumers treat a syslog-write failure as an I/O problem rather than a
// generic internal software error (70).
const exitIOErr int = 74

var (
	// AddrEmpty is returned when New receives an empty network address.
	AddrEmpty = errs.Define(CodeSyslogAddrEmpty, "SYSLOG_ADDR_EMPTY",
		"Syslog sink requires a non-empty address",
		"service/logger/sink/syslog.New called with empty address")

	// ProtoInvalid is returned when New receives an unsupported network.
	ProtoInvalid = errs.Define(CodeSyslogProtoInvalid, "SYSLOG_PROTO_INVALID",
		"Syslog sink supports only 'udp' or 'tcp'",
		"service/logger/sink/syslog.New called with unsupported network")

	// DialFailed wraps a net.Dial failure at construction time.
	DialFailed = errs.Define(CodeSyslogDialFailed, "SYSLOG_DIAL_FAILED",
		"Syslog sink could not dial the destination",
		"service/logger/sink/syslog.New: net.Dial returned an error",
		errs.WithExitCode(exitIOErr))

	// WriteFailed wraps the underlying net.Conn.Write error at Write time.
	WriteFailed = errs.Define(CodeSyslogWriteFailed, "SYSLOG_WRITE_FAILED",
		"Syslog write failed",
		"service/logger/sink/syslog.Write underlying net.Conn returned an error",
		errs.WithExitCode(exitIOErr))

	// CloseFailed wraps the underlying net.Conn.Close error at Close time.
	CloseFailed = errs.Define(CodeSyslogCloseFailed, "SYSLOG_CLOSE_FAILED",
		"Syslog close failed",
		"service/logger/sink/syslog.Close underlying net.Conn.Close returned an error",
		errs.WithExitCode(exitIOErr))
)
