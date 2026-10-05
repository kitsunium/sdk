package trace

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

// Normalized returns the event a span actually records: attributes sorted,
// validated and owned, so the caller's slice cannot be mutated afterwards
// through the span.
func (e EventValue) Normalized() EventValue {
	//: SortAttrs panics on an unusable set, at the call site that wrote it.
	return EventValue{Time: e.Time, Name: e.Name, Attrs: SortAttrs(e.Attrs)}
}
