// Package trace — the inbound HTTP middleware.
package trace

import (
	"net/http"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coretrace "github.com/kitsunium/sdk/internal/core/trace"
)

// The OpenTelemetry HTTP semantic-convention attribute keys this middleware
// records. They are dotted, which is the spelling the conventions SPECIFY — and
// the reason the Prometheus metrics connector refuses them (ADR 0044 §Decision
// 8). OTLP carries them unchanged.
const (
	// HTTPRequestMethodKey is the request method, uppercase.
	HTTPRequestMethodKey string = "http.request.method"
	// HTTPResponseStatusCodeKey is the response status.
	HTTPResponseStatusCodeKey string = "http.response.status_code"
	// URLPathKey is the request path. It is the ESCAPED form — see the
	// middleware comment for why the decoded one is not recorded.
	URLPathKey string = "url.path"
	// URLSchemeKey is "http" or "https".
	URLSchemeKey string = "url.scheme"
	// URLFullKey is the whole outbound URL, recorded on CLIENT spans only.
	URLFullKey string = "url.full"
	// ServerAddressKey is the host a client called, or a server was called on.
	ServerAddressKey string = "server.address"
	// ErrorTypeKey names what went wrong when a call produced no response.
	ErrorTypeKey string = "error.type"
)

// httpServerErrorFloor is the lowest status a SERVER span reports as an error.
//
// 4xx is deliberately NOT an error on a server span, and the OpenTelemetry HTTP
// conventions say so: a 404 is the caller asking for something that is not
// there, which is the server working correctly. Marking it ERROR would make
// every scanner probing for /wp-admin light up a service's error rate.
const httpServerErrorFloor int = 500

// ServerMiddleware returns a middleware that traces every inbound request.
//
// It is a corenet.Middleware[http.Handler] — the SDK's OWN middleware type,
// instantiated at http.Handler — so it composes with corenet.Chain and drops
// straight onto a group:
//
//	srv.Group("api", server.Listen("tcp", ":8443")).
//	    HandleHTTP(corenet.Chain(mux, trace.ServerMiddleware(tracer)))
//
// It decorates an http.Handler rather than the group's ConnHandler because a
// traceparent is an HTTP HEADER: the connection middleware sees bytes, and there
// is no header at that level to extract. That is a statement about where the
// propagation format lives, not a gap in the net domain.
//
// # What it does, in order
//
//  1. EXTRACTS the span context from the request headers. A malformed or absent
//     traceparent silently starts a new trace — which is what W3C Trace Context
//     §4.3 requires, and never a rejected request: the header is written by a
//     stranger, so failing on it is a denial of service with extra steps.
//  2. Starts a SERVER span, named by the request METHOD alone.
//  3. Records the status and marks 5xx as an error.
//
// # Why the span is named "GET" and not "GET /users/42"
//
// A span name is a low-cardinality label a backend GROUPS on. A path carries
// identifiers, so "GET /users/42" and "GET /users/43" are two operations to a
// backend and one to a human — and a service with a million users has a million
// span names, which is how a tracing bill becomes a story. The conventions ask
// for "{method} {route}", where the ROUTE is the template; this SDK does not
// route, so it has no template to use and does not invent one from the path. A
// caller who has a router records it themselves, on the span this middleware
// already put in the context.
//
// The PATH is recorded as an attribute instead, where high cardinality is
// affordable, and it is the ESCAPED form for the reason corenet.RequestValue
// gives: url.URL.Path is already percent-decoded, so a "%2e%2e" that an upstream
// reinterprets as ".." would be recorded as something it is not.
func ServerMiddleware(tracer coretrace.Tracer) corenet.Middleware[http.Handler] {
	//: the returned decorator closes over the tracer alone.
	return func(next http.Handler) http.Handler {
		//: a nil tracer would panic per request, so it is refused here — once,
		//: at wiring time — by handing back the undecorated handler.
		if tracer == nil {
			//: unchanged behaviour rather than a per-request panic.
			return next
		}
		//: one handler per decoration; the closure holds tracer and next.
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			//: serve the request under a SERVER span.
			serveTraced(tracer, next, w, r)
		})
	}
}

// serveTraced runs next under a SERVER span extracted from r's headers.
func serveTraced(tracer coretrace.Tracer, next http.Handler, w http.ResponseWriter, r *http.Request) {
	//: the upstream's context, or the invalid zero value — §4.3's "restart".
	//: r.Header is passed DIRECTLY: http.Header's Get/Set pair is exactly
	//: coretrace.Carrier, which is what that interface was shaped against, so
	//: there is no adapter here and none to keep in step.
	parent := coretrace.Extract(r.Header)
	//: the parent goes on the context so Start inherits it exactly as it would
	//: inherit a local one; there is one inheritance path, not two.
	ctx := coretrace.ContextWithSpanContext(r.Context(), parent)
	//: the attributes a Sampler may need are the ones known before the handler
	//: runs, which is why they are passed at Start rather than set later.
	ctx, span := tracer.Start(ctx, r.Method, coretrace.SpanParams{
		Kind: coretrace.SpanKindServer,
		Attrs: []coremetrics.AttrValue{
			coremetrics.String(HTTPRequestMethodKey, r.Method),
			coremetrics.String(URLPathKey, r.URL.EscapedPath()),
			coremetrics.String(URLSchemeKey, requestScheme(r)),
			coremetrics.String(ServerAddressKey, r.Host),
		},
	})
	//: the span closes when the handler returns, panic included — a handler
	//: that panics is exactly the one whose span is worth having.
	defer span.End()
	//: capture the status without hiding any capability of the writer.
	recorder := &statusRecorder{ResponseWriter: w}
	//: the handler sees the span through the context.
	next.ServeHTTP(recorder, r.WithContext(ctx))
	//: a handler that wrote nothing still produced a 200 — net/http writes one.
	status := recorder.resolvedStatus()
	//: the outcome, recorded on the span the backend will render.
	span.SetAttrs(coremetrics.Int64(HTTPResponseStatusCodeKey, int64(status)))
	//: 5xx is this service's failure; 4xx is the caller's, and is not marked.
	if status >= httpServerErrorFloor {
		//: the status is already an attribute, so the message adds nothing a
		//: reader does not have — and a free-text message is the one field an
		//: operator reads verbatim.
		span.SetStatus(coretrace.StatusError, "")
	}
}

// requestScheme reports the scheme an inbound request arrived on.
//
// r.URL.Scheme is empty on a server request — net/http fills it only for a
// client — so TLS presence is the only fact available here. A forwarded-proto
// header is deliberately NOT read: it is caller-controlled, and a proxy that
// sets it correctly is a deployment fact this package cannot verify.
func requestScheme(r *http.Request) string {
	//: a terminated TLS connection is the only positive evidence there is.
	if r.TLS != nil {
		//: https.
		return schemeHTTPS
	}
	//: everything else is reported as plaintext, which is what the socket was.
	return schemeHTTP
}

// statusRecorder wraps an http.ResponseWriter to remember the status.
//
// It implements Unwrap and NOTHING ELSE, and that is the whole design. The
// tempting alternative — declaring Flush and Hijack that forward to the
// underlying writer — would make this wrapper claim BOTH capabilities
// unconditionally, so `w.(http.Hijacker)` would succeed on a writer that cannot
// hijack and the failure would surface as a runtime error inside a protocol
// upgrade rather than as a clean "not supported". That is the exact defect class
// ADR 0047 fixed in the listener engine, re-introduced by a middleware.
//
// http.ResponseController walks the Unwrap chain and asks the REAL writer, which
// is how this SDK's own SSE and WebSocket implementations already find Flush and
// Hijack (see internal/service/net/sse and .../websocket). Cooperating with that
// convention costs one method and preserves every capability truthfully.
type statusRecorder struct {
	http.ResponseWriter
	// status is the code the handler wrote, or 0 when it wrote none.
	status int
}

// WriteHeader records the status and forwards it.
func (w *statusRecorder) WriteHeader(code int) {
	//: the FIRST WriteHeader is the one net/http sends; a second is ignored by
	//: net/http (with a log line), so recording it would misreport the wire.
	if w.status == 0 {
		//: remember what actually went out.
		w.status = code
	}
	//: forward unchanged.
	w.ResponseWriter.WriteHeader(code)
}

// Write records the implicit 200 net/http writes for a handler that never called
// WriteHeader, then forwards.
func (w *statusRecorder) Write(data []byte) (n int, err error) {
	//: an unheadered Write makes net/http send 200 before the body.
	if w.status == 0 {
		//: the status that actually goes on the wire.
		w.status = http.StatusOK
	}
	//: forward unchanged.
	return w.ResponseWriter.Write(data)
}

// Unwrap exposes the wrapped writer to http.ResponseController, which is how
// Flush, Hijack and the deadline setters reach the real writer — see the type
// comment for why they are not forwarded by hand.
func (w *statusRecorder) Unwrap() http.ResponseWriter {
	//: the writer this one decorates.
	return w.ResponseWriter
}

// resolvedStatus reports the status that reached the wire: what the handler
// wrote, or the 200 net/http writes for a handler that returned having written
// nothing at all.
func (w *statusRecorder) resolvedStatus() int {
	//: a handler that never wrote still produced a response.
	if w.status == 0 {
		//: net/http's own implicit status.
		return http.StatusOK
	}
	//: what the handler wrote.
	return w.status
}
