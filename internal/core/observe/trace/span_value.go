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

// isRoot is SpanValue.IsRoot's body: decl_gen.go writes SpanValue.IsRoot, from the
// design, as one call of it.
func (s SpanValue) isRoot() bool {
	//: an invalid parent context is the schema's "no parent".
	return !s.Parent.IsValid()
}
