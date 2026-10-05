// Package logger — bridges the trace domain to the logging domain, so a log
// line emitted inside a span carries that span's identity and an operator
// holding a trace_id can find the logs that belong to it.
//
// This file is the ONLY place in the SDK where logging and tracing meet, and
// that placement is the design (ADR 0062). internal/service/observe/logger and
// internal/service/observe/trace are siblings, and neither imports the other because
// the two domains meet only here, at the top layer; internal/core/observe/logger is
// kept stdlib-only so a consumer who only wants a line on stderr does not
// compile the trace model. pkg/v1 is the layer that knows both domains
// already, so the binding lives here and nowhere else.
package logger

import (
	"context"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
)

// TraceContextFromContext reads the span identity carried by ctx and returns
// it in the shape a log record carries.
//
// It is the adapter between [github.com/kitsunium/sdk/pkg/v1/observe/trace]'s
// SpanContext and the logger's TraceContext: the two identifiers, without the
// flags and the tracestate, because a log line answers "which span produced
// this?" and nothing else.
//
// An absent, unsampled-and-absent or malformed span context yields the zero
// value, and the encoders then emit no trace_id and no span_id at all — never
// an empty string and never the all-zero identifier, both of which W3C Trace
// Context §3.2.2.3/§3.2.2.4 declare invalid.
//
// It allocates nothing: the context walk returns a value type, and the two
// identifiers are fixed-size arrays copied by assignment.
func TraceContextFromContext(ctx context.Context) TraceContext {
	//: the trace domain owns the context key; this is the only reader.
	spanContext := coretrace.SpanContextFromContext(ctx)
	//: an invalid span context is the normal state of code outside a request.
	if !spanContext.IsValid() {
		//: the zero value renders nothing at all.
		return TraceContext{}
	}
	//: the two array conversions are also the compile-time proof that the
	//: logger and the trace domain agree on the identifier widths — a
	//: mismatch would not build.
	return TraceContext{
		TraceID: [corelogger.TraceIDLen]byte(spanContext.TraceID),
		SpanID:  [corelogger.SpanIDLen]byte(spanContext.SpanID),
	}
}
