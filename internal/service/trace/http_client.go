// Package trace — the outbound HTTP middleware.
package trace

import (
	"net/http"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coretrace "github.com/kitsunium/sdk/internal/core/trace"
)

// httpClientErrorFloor is the lowest status a CLIENT span reports as an error.
//
// 4xx IS an error on a client span, unlike on a server span: the caller asked
// for something and did not get it, which is a failure of the call whatever it
// says about whose fault it is. The OpenTelemetry HTTP conventions make exactly
// this asymmetry, and it is the one thing a reader is most likely to assume is a
// copy-paste slip between this constant and httpServerErrorFloor.
const httpClientErrorFloor int = 400

// ClientMiddleware returns a middleware that traces every outbound request and
// INJECTS the traceparent into it.
//
// It is a corenet.Middleware[http.RoundTripper] — the SDK's own middleware type
// again, this time instantiated at the outbound seam — so it composes with
// corenet.Chain and wraps the guarded client's transport:
//
//	c, err := client.New(cfg, identity, policy, hook)
//	c.HTTP().Transport = corenet.Chain(c.HTTP().Transport, trace.ClientMiddleware(tracer))
//
// It decorates the ROUND TRIPPER rather than the call site, and that placement is
// the same argument the net domain makes for putting its Policy there: a caller
// cannot construct a request that skips the transport, so "every egress carries a
// traceparent" stops being a convention the next contributor has to remember.
//
// # The request is CLONED
//
// http.RoundTripper's contract says an implementation "should not modify the
// request", and the reason is not pedantry: net/http retries an idempotent
// request on a fresh connection using the SAME *http.Request, so a header set in
// place would be observed by a caller inspecting the request afterwards, and a
// span-per-attempt would write a different traceparent onto a request the caller
// still holds. Cloning costs one header map per call and makes the middleware
// re-entrant.
func ClientMiddleware(tracer coretrace.Tracer) corenet.Middleware[http.RoundTripper] {
	//: the returned decorator closes over the tracer alone.
	return func(next http.RoundTripper) http.RoundTripper {
		//: a nil tracer or a nil transport hands back what it was given, once,
		//: at wiring time — rather than panicking on every request.
		if tracer == nil || next == nil {
			//: unchanged behaviour.
			return next
		}
		//: the concrete decorator.
		return &tracedRoundTripper{tracer: tracer, next: next}
	}
}

// tracedRoundTripper is the http.RoundTripper ClientMiddleware installs.
type tracedRoundTripper struct {
	// tracer starts the CLIENT span.
	tracer coretrace.Tracer
	// next is the transport that actually performs the call.
	next http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
//
// The span ends when the RESPONSE HEADERS arrive, not when the body has been
// read, and that is a deliberate boundary rather than a convenience: RoundTrip
// returns before the body exists, so there is nothing here that could observe the
// body's end. It is the same boundary corenet.CallValue.Duration documents for
// the client's own hook — "the upstream's time to first answer, which is the one
// a slow upstream and a large payload do not share".
func (t *tracedRoundTripper) RoundTrip(request *http.Request) (response *http.Response, err error) {
	//: the CLIENT span is a child of whatever scope the caller is in.
	ctx, span := t.tracer.Start(request.Context(), request.Method, coretrace.SpanParams{
		Kind: coretrace.SpanKindClient,
		Attrs: []coremetrics.AttrValue{
			coremetrics.String(HTTPRequestMethodKey, request.Method),
			coremetrics.String(URLFullKey, request.URL.String()),
			coremetrics.String(ServerAddressKey, request.URL.Host),
		},
	})
	//: the span closes on every exit, transport fault included.
	defer span.End()
	//: clone before touching a header — see the ClientMiddleware comment.
	outbound := request.Clone(ctx)
	//: an unsampled span still injects, with the sampled bit CLEAR, so the
	//: upstream inherits the decision instead of taking a contradictory one.
	coretrace.Inject(span.SpanContext(), outbound.Header)
	//: perform the call.
	answer, callErr := t.next.RoundTrip(outbound)
	//: a transport fault produced no status to record.
	if callErr != nil {
		//: error.type names the failure without echoing the upstream's text,
		//: which may carry an internal address.
		span.SetAttrs(coremetrics.String(ErrorTypeKey, "transport"))
		//: the message is left empty for the reason the server side leaves it
		//: empty: a free-text field is read verbatim by an operator.
		span.SetStatus(coretrace.StatusError, "")
		//: hand the fault back unchanged — a middleware must not reshape it.
		return nil, callErr
	}
	//: the status the upstream answered with.
	span.SetAttrs(coremetrics.Int64(HTTPResponseStatusCodeKey, int64(answer.StatusCode)))
	//: on a CLIENT span a 4xx is a failed call — see httpClientErrorFloor.
	if answer.StatusCode >= httpClientErrorFloor {
		//: recorded without a message.
		span.SetStatus(coretrace.StatusError, "")
	}
	//: hand the response back untouched, body unread.
	return answer, nil
}
