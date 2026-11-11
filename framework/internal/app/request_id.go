package app

import (
	"context"
	"net/http"

	"github.com/kitsunium/sdk/pkg/v1/app/id"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
)

const (
	// requestIDHeader carries a request's identifier, in and out.
	requestIDHeader string = "X-Request-Id"
	// maxRequestID bounds the identifier a caller may send: a header can be as
	// long as a client likes, and the identifier is written to every log
	// line that names the request.
	maxRequestID int = 128
)

// requestKey carries what the HTTP middleware knows of the request a context
// serves.
type requestKey struct{}

// requestInfo is a request's identity: its identifier, and the span context
// of the span the endpoint or the frontend began for it — which the
// middleware, outside that span, reads when the request ends. The span begins
// in the goroutine serving the request, the middleware reads after that
// goroutine's handler returned: no lock.
type requestInfo struct {
	id   string
	span trace.SpanContext
}

// RequestID returns the identifier of the request ctx serves: the X-Request-Id
// its caller sent — a short run of letters, digits, '.', '_' and '-', else
// ignored — or a UUIDv7 the framework minted. The response carries it as
// X-Request-Id, an error body as requestId, the span as http.request_id and the
// access record as request_id. It is "" outside an HTTP request.
func RequestID(ctx context.Context) string {
	if ri := requestInfoOf(ctx); ri != nil {
		return ri.id
	}
	return ""
}

// requestInfoOf is the identity of the request ctx serves, nil outside one.
func requestInfoOf(ctx context.Context) *requestInfo {
	ri, _ := ctx.Value(requestKey{}).(*requestInfo)
	return ri
}

// requestIDOf is the identifier h carries, when it is one this framework
// accepts: the header is the caller's, so a value that could forge a log
// line, or grow one without bound, is not taken.
func requestIDOf(h http.Header) (rid string, ok bool) {
	v := h.Get(requestIDHeader)
	if v == "" || len(v) > maxRequestID {
		return "", false
	}
	for i := range len(v) {
		if !requestIDByte(v[i]) {
			return "", false
		}
	}
	return v, true
}

// requestIDByte reports whether c may be part of a request identifier.
func requestIDByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	default:
		return c == '.' || c == '_' || c == '-'
	}
}

// mintRequestID is a UUIDv7 for a request whose caller sent no usable
// identifier; not ok only when the system's randomness fails, in which case
// the request goes without one rather than not at all.
func mintRequestID() (rid string, ok bool) {
	rid, err := id.UUIDv7()
	if err != nil {
		return "", false
	}
	return rid, true
}

// joinRequest says what request ctx serves is joining the span sp: its
// identifier is the span's http.request_id, and the span is the one the access
// record is correlated with. It does nothing outside an HTTP request.
func joinRequest(ctx context.Context, sp *span) {
	ri := requestInfoOf(ctx)
	if ri == nil {
		return
	}
	ri.span = trace.SpanContextFromContext(ctx)
	if ri.id == "" {
		return
	}
	sp.attr("http.request_id", ri.id)
	if sp.sdk != nil {
		sp.sdk.SetAttrs(trace.String("http.request_id", ri.id))
	}
}

// remoteTrace is ctx continuing the trace a caller's W3C traceparent names;
// a missing or malformed header leaves ctx as it is, and the span that
// follows starts a trace.
func remoteTrace(ctx context.Context, h http.Header) context.Context {
	if sc := trace.Extract(h); sc.IsValid() {
		return trace.ContextWithSpanContext(ctx, sc)
	}
	return ctx
}
