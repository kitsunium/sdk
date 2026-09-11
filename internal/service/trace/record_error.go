// Package trace — recording an error on a span.
package trace

import (
	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// RecordError records err on span as the OpenTelemetry-conventional `exception`
// event, and marks the span's status ERROR.
//
// It is a HELPER over the frozen core/trace.Span port, not a method on it, and
// the distinction is ADR 0039 in practice: what an error's TYPE is, whether a
// recorded error should also set the status, and whether a stack trace belongs
// on the span are all judgements about the caller's error model. A method would
// freeze this SDK's answer into a port every downstream implementer has to
// satisfy; a helper leaves the port at five methods and lets a caller who
// disagrees write their own three lines.
//
// The `exception.type` attribute carries the errs DOTTED-QUAD code when err is
// an SDK error, and is omitted otherwise. A code is the stable identity of a
// failure — a reason string can be reworded, a code cannot — and it is what an
// operator greps for across a trace, a log line and an alert. A foreign error has
// no such identity here, and `%T` would name a Go type nobody outside the process
// can act on.
//
// The `exception.message` attribute is err.Error() VERBATIM. On an SDK error that
// is the wire-safe rendering (bracket header plus Public, never Private); on a
// foreign error it is whatever the caller's error says. The rule is the log-line
// rule: an error whose text carries a secret does not belong on a span, for the
// same reason it does not belong in a log.
//
// `exception.stacktrace` is never set. A Go error carries no stack, and one taken
// here would name the goroutine that RECORDED the error rather than the one that
// produced it — a plausible wrong answer, which is worse than an absent one.
func RecordError(span coretrace.Span, err error) {
	//: nothing to record on, or nothing to record.
	if span == nil || err == nil {
		//: no event, no status change.
		return
	}
	//: the message is always present; the type is only present when it exists.
	attrs := []coremetrics.AttrValue{coremetrics.String(coretrace.ExceptionMessageKey, err.Error())}
	//: an SDK error carries a dotted-quad identity; a foreign one does not.
	if code, ok := errs.CodeOf(err); ok {
		//: the stable identity, in the MM.LL.PP.SS spelling.
		attrs = append(attrs, coremetrics.String(coretrace.ExceptionTypeKey, code.String()))
	}
	//: the conventional event name a backend renders as an error.
	span.AddEvent(coretrace.ExceptionEventName, attrs...)
	//: the message is left empty on the STATUS: it is already an attribute of
	//: the event, and duplicating it would put the same text in two places a
	//: backend renders differently.
	span.SetStatus(coretrace.StatusError, "")
}
