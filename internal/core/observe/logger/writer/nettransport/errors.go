// Package nettransport — declares the sentinels returned by the network sink's
// constructor and Write path. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form. No value parsed from a config map or a record is ever
// echoed into these errors (secret gate): only the network and a fixed private
// string are attached.
package nettransport

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitIOErr matches sysexits EX_IOERR — a network transport failure is an I/O
// problem, not a generic internal software error (70).
const exitIOErr int = 74

var (
	// NetTransportDialFailed wraps a net.Dial failure (tcp/udp) or a rejected
	// HTTP endpoint at construction time.
	NetTransportDialFailed = errs.Define(CodeNetTransportDialFailed, "NET_TRANSPORT_DIAL_FAILED",
		"Network transport could not establish its destination",
		"service/observe/logger/writer/nettransport: net.Dial failed or the HTTP endpoint was invalid",
		errs.WithExitCode(exitIOErr))

	// NetTransportWriteFailed wraps a per-record send failure: a net.Conn.Write
	// error or a non-2xx HTTP response.
	NetTransportWriteFailed = errs.Define(CodeNetTransportWriteFailed, "NET_TRANSPORT_WRITE_FAILED",
		"Network transport write failed",
		"service/observe/logger/writer/nettransport: the underlying transport returned an error",
		errs.WithExitCode(exitIOErr))
)
