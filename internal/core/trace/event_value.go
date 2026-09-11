// Package trace — Event: a timestamped point inside a span.
package trace

import (
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// ExceptionEventName and the three attribute keys around it are the OpenTelemetry
// semantic convention for recording an error on a span.
//
// They are constants here rather than strings at a call site because the whole
// value of a convention is that two producers spell it identically: a backend
// renders an event named exactly "exception" as an error, and renders
// "Exception" as an event nobody will ever look at.
const (
	// ExceptionEventName is the reserved event name for a recorded error.
	ExceptionEventName string = "exception"
	// ExceptionTypeKey names the error's type.
	ExceptionTypeKey string = "exception.type"
	// ExceptionMessageKey names the error's message.
	ExceptionMessageKey string = "exception.message"
	// ExceptionStacktraceKey names the error's stack trace. This SDK never
	// fills it: a Go error carries no stack, and inventing one from the
	// recording site would name the wrong goroutine.
	ExceptionStacktraceKey string = "exception.stacktrace"
)

// EventValue is one thing that happened at an instant during a span, rather than
// over an interval — `opentelemetry/proto/trace/v1.Span.Event`.
//
// An event is not a cheap child span, and the distinction is worth stating
// because the wrong choice is invisible until a trace is unreadable: an event
// has no identity, no duration and no children, so nothing can be a child of it
// and nothing can link to it. Use one for a moment worth marking on a span's
// timeline (a retry, a cache miss, an exception); use a child span for work that
// took time.
type EventValue struct {
	// Time is when it happened. An unset instant encodes as 0, which is what
	// the schema means by an unknown timestamp.
	Time time.Time
	// Name is the event's low-cardinality label.
	Name string
	// Attrs are the event's typed dimensions, sorted by Key.
	Attrs []coremetrics.AttrValue
}

// Normalized returns the event a span actually records: attributes sorted,
// validated and owned, so the caller's slice cannot be mutated afterwards
// through the span.
func (e EventValue) Normalized() EventValue {
	//: SortAttrs panics on an unusable set, at the call site that wrote it.
	return EventValue{Time: e.Time, Name: e.Name, Attrs: coremetrics.SortAttrs(e.Attrs)}
}
