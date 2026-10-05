// Package syslog — holds the Config struct consumed by
// NewWithConfig. Pulled into its own file per the SDK's
// one-exported-struct-per-file convention.
//
// Package syslog — computes the RFC5424 priority value
// (facility * 8 + severity) consumed by the framing layer. Pulled into
// its own file so syslog_sink.go stays focused on the Sink contract.
//
// Package syslog implements a network sink that ships records to a syslog
// daemon via UDP or TCP. The wire format is a minimal RFC5424 envelope:
//
//	<PRI>1 - - - - - - <payload>
//
// PRI is computed from the record's level + the default USER facility.
// Hostname, app-name, procid, msgid, and structured-data slots are fixed
// to "-" to keep the producer side allocation-friendly; consumers that
// need richer envelopes wrap this sink with their own framing.
//
// Use case: ship logs to journald / rsyslog / a central syslog collector
// over the standard 514/UDP port.
package syslog
