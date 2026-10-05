package trace

import (
	"time"
)

// Duration reports how long the span covered, or zero when it never ended.
func (s SpanValue) Duration() time.Duration {
	//: a span with no end has no duration to report — not a negative one.
	if s.StartTime.IsZero() || s.EndTime.IsZero() || s.EndTime.Before(s.StartTime) {
		//: zero, which is what "unknown" is spelled as here.
		return 0
	}
	//: the closed interval.
	return s.EndTime.Sub(s.StartTime)
}

// IsRoot reports whether the span has no parent.
func (s SpanValue) IsRoot() bool {
	//: an invalid parent context is the schema's "no parent".
	return !s.Parent.IsValid()
}
