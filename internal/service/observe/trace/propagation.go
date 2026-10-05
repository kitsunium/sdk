package trace

import coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"

// Inject writes context into carrier as a traceparent, plus a tracestate when
// the list is non-empty.
//
// An INVALID context writes nothing at all — not an empty header, not a
// zero-filled one. §3.2.2.3/§3.2.2.4 require a receiver to ignore an all-zero
// identifier, so emitting one would spend a header on a value the next hop is
// obliged to discard, and would make "we lost the context here" indistinguishable
// from "we never had one".
//
// An empty tracestate is likewise omitted rather than set blank, because §4.2
// says a tracestate without a traceparent "is invalid and MUST be discarded" —
// the two headers are a pair, and a blank one is not half of it.
func Inject(context coretrace.SpanContextValue, carrier coretrace.Carrier) {
	//: the identity, in the only version this SDK produces — and the ok is the
	//: whole guard: a context that names no joinable span has no header.
	header, ok := FormatTraceParent(context)
	//: nothing to propagate, so nothing is written.
	if !ok {
		//: the carrier is left exactly as it was.
		return
	}
	//: one header for the identity.
	carrier.Set(TraceParentHeader, header)
	//: the vendor list travels verbatim, and only when there is one.
	if context.State.Len() > 0 {
		//: rendered without the optional whitespace the grammar allows.
		carrier.Set(TraceStateHeader, context.State.String())
	}
}

// Extract reads a span context out of carrier, returning the INVALID zero value
// when there is nothing usable to read.
//
// It returns no error, and that is a decision rather than an omission. §4.3
// prescribes exactly one behaviour for a malformed traceparent — "the vendor
// creates a new traceparent header and deletes tracestate" — so there is nothing
// for a caller to decide and nothing for them to handle. Handing them an error
// would invite the one response the specification forbids: failing a request
// because a stranger wrote a bad header. ParseTraceParent is the typed-error
// form, for a caller who is diagnosing rather than serving.
//
// Three consequences of that rule, each visible here:
//
//   - A malformed traceparent DROPS the tracestate with it. The vendor list
//     describes a trace that this process is about to replace, so keeping it
//     would attach somebody else's state to a brand-new trace id.
//   - A malformed TRACESTATE does not drop the traceparent. §4.3 makes
//     validating tracestate a MAY and permits discarding just that header, and
//     the parent is independently well-formed — losing a valid parent over an
//     unreadable vendor list would break the trace to protect an annotation.
//   - Two traceparent headers merge, under the RFC's rule on repeated header
//     fields, into "value1,value2", which fails the grammar and restarts the
//     trace. That is the correct outcome: two upstreams claiming different
//     parents is not a case where either may be believed.
func Extract(carrier coretrace.Carrier) coretrace.SpanContextValue {
	//: the parent decides whether anything else is read at all.
	context, err := ParseTraceParent(carrier.Get(TraceParentHeader))
	//: absent, malformed, or version ff — start a new trace.
	if err != nil {
		//: the invalid zero value, which is also "no context".
		return coretrace.SpanContextValue{}
	}
	//: the vendor list is optional and independently fallible.
	state, stateErr := ParseTraceState(carrier.Get(TraceStateHeader))
	//: an unreadable list is discarded; the parent survives it.
	if stateErr != nil {
		//: the identity alone, with an empty list.
		return context
	}
	//: parent plus vendor state, both as the upstream wrote them.
	return context.WithState(state)
}
