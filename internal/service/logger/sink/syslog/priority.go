// Package syslog: priority.go computes the RFC5424 priority value
// (facility * 8 + severity) consumed by the framing layer. Pulled into
// its own file so syslog_sink.go stays focused on the Sink contract.
package syslog

import "github.com/kitsunium/sdk/internal/core/logger/level"

// facilityUser is the default RFC5424 facility code (USER, value 1) used
// when callers do not override it. The full enum is documented in RFC5424
// §6.2.1 — we ship just USER today and let consumers configure the rest
// in a future commit.
const facilityUser int = 1

// facilityShift is the multiplier applied to the facility code per
// RFC5424 (priority = facility * 8 + severity).
const facilityShift int = 8

// severityError matches RFC5424 severity 3 (Error condition).
const severityError int = 3

// severityWarning matches RFC5424 severity 4 — used as the default for
// records whose level falls outside the four mapped buckets.
const severityWarning int = 4

// severityInfo matches RFC5424 severity 6 (Informational).
const severityInfo int = 6

// severityDebug matches RFC5424 severity 7 (Debug).
const severityDebug int = 7

// severityFor maps a corelogger.Level to its RFC5424 severity number.
//
// Params:
//   - lv: corelogger severity from the originating record.
//
// Returns:
//   - sev: the matching RFC5424 severity in [0, 7].
func severityFor(lv level.Level) (sev int) {
	//: dispatch on the four documented levels; everything else maps to warning.
	switch {
	//: error → RFC5424 severity 3 (Error).
	case lv >= level.Error:
		//: highest severity we render today.
		return severityError
	//: warn → RFC5424 severity 4 (Warning).
	case lv >= level.Warn:
		//: mid-tier severity.
		return severityWarning
	//: info → RFC5424 severity 6 (Informational).
	case lv >= level.Info:
		//: routine operational events.
		return severityInfo
	//: debug or below → RFC5424 severity 7 (Debug).
	default:
		//: tracing severity for development paths.
		return severityDebug
	}
}

// priorityFor returns the RFC5424 PRI value for the supplied level using
// the default USER facility.
//
// Params:
//   - lv: corelogger severity from the originating record.
//
// Returns:
//   - pri: the RFC5424 PRI value (facility * 8 + severity).
func priorityFor(lv level.Level) (pri int) {
	//: combine facility + severity per the RFC5424 PRI formula.
	return facilityUser*facilityShift + severityFor(lv)
}
