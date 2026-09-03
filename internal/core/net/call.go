// Package net — the completed-call observation record.
package net

import "time"

// CallValue records one completed outbound call for observation, so a caller
// can log, meter or trace every egress without wrapping each call site by hand.
//
// It is handed to a CallHook once the response body is FINISHED WITH — read to
// the end, failed, or closed — not when the headers arrive. A body's size is
// only known after it has been read, and an over-sized body or a failed close
// happens later still, so a record emitted at the headers could carry neither:
// it reported every call as a clean success of zero bytes. A refused request
// and an unreachable upstream are still reported immediately, because in
// neither case is there a body to wait for.
type CallValue struct {
	// Method is the uppercase HTTP method.
	Method string
	// Host is the authority the call was sent to.
	Host string
	// Path is the request path.
	Path string
	// Status is the HTTP status code, or zero when the call produced no response.
	Status int
	// Bytes is the number of response body bytes read.
	Bytes int64
	// Duration is the wall-clock time from request start to response headers.
	// It deliberately excludes the body: the record is emitted once the body is
	// done with, but this field measures the upstream's time to first answer,
	// which is the one a slow upstream and a large payload do not share.
	Duration time.Duration
	// Err is what ended the call: a policy refusal, a transport failure, the
	// response ceiling, or a failed close. Nil when the body was read in full.
	Err error
}
