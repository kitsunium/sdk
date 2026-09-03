// Package net — the completed-call observation record.
package net

import "time"

// CallValue records one completed outbound call for observation. It is handed to
// a CallHook after the response headers are available, so a caller can log,
// meter, or trace every egress without wrapping each call site by hand.
type CallValue struct {
	// Method is the uppercase HTTP method.
	Method string
	// Host is the authority the call was sent to.
	Host string
	// Path is the request path.
	Path string
	// Status is the HTTP status code, or zero when the call produced no response.
	Status int
	// Bytes is the number of response body bytes read, or zero when unread.
	Bytes int64
	// Duration is the wall-clock time from request start to response headers.
	Duration time.Duration
	// Err is the failure that ended the call, or nil on success.
	Err error
}
