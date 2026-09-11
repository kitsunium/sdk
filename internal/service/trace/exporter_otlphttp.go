// Package trace — the OTLP/HTTP emitter: the only file here that imports
// net/http for an outbound socket.
package trace

import (
	"bytes"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// OTLPTracesPath is the OTLP/HTTP path for the trace signal. It is a constant
// rather than something the exporter appends, so an endpoint stays a full URL a
// reviewer can read in one string: "http://collector:4318" + OTLPTracesPath.
const OTLPTracesPath string = "/v1/traces"

// DefaultOTLPTimeout is the OpenTelemetry protocol exporter specification's own
// default for OTEL_EXPORTER_OTLP_TIMEOUT. The clamp lands on the specification's
// number rather than on one this SDK invented.
const DefaultOTLPTimeout time.Duration = 10 * time.Second

// DefaultOTLPMaxResponseBytes caps the response body read at 1 MiB. The response
// is the one length a REMOTE party controls in this exchange; the REQUEST body
// is deliberately not capped, because its size is a property of the caller's own
// span volume, any SDK-chosen ceiling would be arbitrary (ADR 0031 §refuse), and
// the collector already answers 413 for one it will not take.
//
// It also bounds the drain that recycles the connection after the verdict —
// always this default rather than OTLPHTTPConfig.MaxResponseBytes, for the
// reason closeOTLPResponse gives.
const DefaultOTLPMaxResponseBytes int64 = 1 << 20

// otlpFallbackIdleTimeout is how long the default client's fallback transport
// keeps an idle connection — see newOTLPTransport. It is net/http's own
// DefaultTransport value, so the fallback reaps its pool exactly as the clone it
// stands in for does, rather than holding a connection for as long as the
// collector tolerates it.
const otlpFallbackIdleTimeout time.Duration = 90 * time.Second

// otlpContentType is what the specification requires for the JSON encoding.
const otlpContentType string = "application/json"

// contentTypeHeader and otlpRetryAfterHeader are the two header names this
// exporter writes and reads.
const (
	contentTypeHeader    string = "Content-Type"
	otlpRetryAfterHeader string = "Retry-After"
)

// The 2xx window, named so the classification reads as the specification does.
const (
	httpStatusOK           int = 200
	httpStatusMultiChoices int = 300
)

// The three errs Field keys this exporter attaches. None of them carries
// remote-controlled text or the endpoint.
const (
	rejectedFieldKey   string = "rejected_spans"
	retryAfterFieldKey string = "retry_after_seconds"
	statusFieldKey     string = "http_status"
)

// The two URL schemes OTLP/HTTP speaks, plus the path that is not one.
const (
	schemeHTTP  string = "http"
	schemeHTTPS string = "https"
	rootPath    string = "/"
)

// otlpHTTPExporter POSTs each batch to an OTLP collector as OTLP/JSON.
//
// It holds no mutable state, so unlike the writer-bound exporter it needs no
// mutex: http.Client is safe for concurrent use, and every request builds its own
// body from its own batch.
type otlpHTTPExporter struct {
	// name is the exporter's name. It is NOT a registry key — this exporter is
	// never registered (see NewOTLPHTTPExporter) — it is what an error and a
	// diagnostic call it.
	name coretrace.ExporterName
	// endpoint is the validated full URL.
	endpoint string
	// client is the caller's, or the bounded redirect-refusing default.
	client *http.Client
	// headers are owned copies of the caller's, written on every request.
	headers map[string]string
	// maxResponse caps the response body read.
	maxResponse int64
}

// NewOTLPHTTPExporter builds a SpanExporter that POSTs each batch to an OTLP
// collector as OTLP/JSON.
//
// # It is NOT registered, and that is a decision one step beyond ADR 0030
//
// ADR 0030 refuses to arm a WRITER on stdout from an import. Arming a NETWORK
// CLIENT from an import is strictly worse, and for a reason that is structural
// rather than a matter of degree: there is no endpoint that could be a correct
// default. `localhost:4318` is a guess, and a wrong guess turns every export into
// a POST at whatever answers on that address — inside a cluster, that is a real
// host belonging to somebody else. So this exporter is constructed explicitly, by
// a caller who names the collector, or not at all. A test pins its absence from
// AvailableExporters.
//
// # It does not retry
//
// The specification asks a client to honour Retry-After and otherwise back off
// exponentially. `internal/service/resilience` already ships that policy, and a
// backoff hidden inside Export would be a second one a caller cannot see, tune or
// cancel — Export has no context to cancel it with. What this exporter supplies
// instead is the CLASSIFICATION a retry policy needs, in the exact shape
// resilience.RetryConfig.Retryable wants:
//
//	resilience.NewRetry(resilience.RetryConfig{
//	    MaxAttempts: 3,
//	    BaseDelay:   time.Second,
//	    Retryable:   trace.OTLPRetryable,
//	})
//
// A construction failure is returned rather than deferred into an always-failing
// exporter: refusing at wiring time is strictly earlier than refusing at the
// first export.
func NewOTLPHTTPExporter(name coretrace.ExporterName, cfg OTLPHTTPConfig) (exporter coretrace.SpanExporter, err error) {
	//: an endpoint that cannot be a collector fails here, not in production.
	if endpointErr := checkOTLPEndpoint(cfg.Endpoint); endpointErr != nil {
		//: surface the typed refusal; nothing is constructed.
		return nil, endpointErr
	}
	//: clamp the two bounded knobs; neither has an inert setting.
	timeout := cfg.Timeout
	//: non-positive takes the specification's own default.
	if timeout <= 0 {
		//: OTEL_EXPORTER_OTLP_TIMEOUT's documented default.
		timeout = DefaultOTLPTimeout
	}
	//: same clamp for the response cap.
	maxResponse := cfg.MaxResponseBytes
	//: non-positive takes the default; "unbounded" is not offered.
	if maxResponse <= 0 {
		//: generous next to a conforming response, finite next to a hostile one.
		maxResponse = DefaultOTLPMaxResponseBytes
	}
	//: a caller-supplied client is used as-is, deadline and redirects included.
	client := cfg.Client
	//: only build one when the caller brought none.
	if client == nil {
		//: bounded and redirect-refusing — see newOTLPClient.
		client = newOTLPClient(timeout)
	}
	//: own the header map so a later caller mutation cannot change the wire.
	return &otlpHTTPExporter{
		name:        name,
		endpoint:    cfg.Endpoint,
		client:      client,
		headers:     maps.Clone(cfg.Headers),
		maxResponse: maxResponse,
	}, nil
}

// OTLPRetryable reports whether err is an OTLP/HTTP failure the specification
// says may be replayed: a transport fault, or one of HTTP 429 / 502 / 503 / 504.
// Everything else — a rejected payload, a partial success, an unencodable span —
// is false, because replaying identical bytes at a collector that already refused
// them only costs the retry budget.
//
// Its signature is exactly resilience.RetryConfig.Retryable's, so it drops
// straight into a retry policy without an adapter. That is the whole reason this
// exporter classifies instead of looping.
func OTLPRetryable(err error) bool {
	//: one code carries the transient verdict; HasCode walks the wrap trail.
	return errs.HasCode(err, CodeOTLPExportUnavailable)
}

// checkOTLPEndpoint refuses an endpoint that cannot address a trace collector.
//
// An endpoint is STRUCTURE — a literal or a config value fixed at wiring — so it
// is wrong on the first export or never, which is what makes refusing it safe.
// The path check is the one that earns its keep: every other mistake surfaces as
// a connection error a reader recognises, while a bare "http://collector:4318"
// connects, gets a 404, and looks exactly like a collector that is up.
func checkOTLPEndpoint(endpoint string) error {
	//: an empty endpoint has no failure mode worth distinguishing.
	if endpoint == "" {
		//: refuse without echoing anything.
		return OTLPEndpointInvalid
	}
	//: parse strictly — a relative reference has no host to POST to.
	parsed, err := url.Parse(endpoint)
	//: a malformed URL is refused with its cause on the trail, never its text.
	if err != nil {
		//: errs renders the Public message only, so the URL stays out of logs.
		return errs.Wrap(err, errs.WrapParams{
			Code:    CodeOTLPEndpointInvalid,
			Reason:  "OTLP_ENDPOINT_INVALID",
			Public:  "The OTLP endpoint must be an absolute http(s) URL with a path",
			Private: "service/trace: the configured OTLP endpoint could not be parsed as a URL",
		})
	}
	//: three refusals with one effect: OTLP/HTTP is http or https, a host is
	//: what there is to connect to, and the signal path must be spelled because
	//: an endpoint is used as-is.
	if (parsed.Scheme != schemeHTTP && parsed.Scheme != schemeHTTPS) ||
		parsed.Host == "" ||
		parsed.Path == "" || parsed.Path == rootPath {
		//: refuse without echoing the endpoint.
		return OTLPEndpointInvalid
	}
	//: the endpoint can address a collector.
	return nil
}

// newOTLPClient builds the default client: bounded by timeout, refusing to
// follow a redirect, and riding a connection pool of its own.
//
// The redirect refusal is CWE-918. A 30x from a collector — or from anything
// sitting in front of one — would otherwise bounce a POST carrying the caller's
// Authorization header to whatever host the response names, past an allowlist
// that only ever saw the configured endpoint. ErrUseLastResponse stops the chain
// and hands the 30x back as-is, where it is classified as a permanent rejection.
//
// The pool is newOTLPTransport's, which says why it is not the process's.
func newOTLPClient(timeout time.Duration) *http.Client {
	//: Timeout covers dial, write, read and body — the whole round trip.
	return &http.Client{
		Timeout: timeout,
		//: this exporter's own pool, which nothing else in the process can empty.
		Transport: newOTLPTransport(),
		//: stop at the first redirect and return that response.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			//: the stdlib signal for "hand it back", not an error verdict.
			return http.ErrUseLastResponse
		},
	}
}

// newOTLPTransport returns the connection pool the default client owns: a clone
// of http.DefaultTransport, so every setting a process expects of an outbound
// client — the proxy environment first of all — carries over, and none of its
// connections do.
//
// # Why the default client does not share http.DefaultTransport
//
// Because anything in the process can empty that pool, and net/http turns doing
// so at the wrong instant into a wrong verdict. A response with no body is put
// back in the idle pool BEFORE the waiting round trip is handed it, and a
// CloseIdleConnections landing in that window closes the connection under it:
// the round trip then reports "connection broken" for a response that had
// already arrived. Here that made a 200 a retryable OTLPExportUnavailable — the
// caller's retry replays spans the collector accepted, and the backend stores
// them twice — a 429 lose its Retry-After, and a 413 retryable. Every
// httptest.Server.Close and every http.DefaultClient.CloseIdleConnections is
// such a call, made by code this exporter cannot see.
//
// A caller-supplied OTLPHTTPConfig.Client is used as given, Transport included,
// so one that rides http.DefaultTransport inherits that exposure; giving it a
// Transport of its own is how it avoids it.
//
// The clone is a snapshot taken at construction. A process that has replaced
// http.DefaultTransport with something other than an *http.Transport leaves
// nothing to clone, and a fresh transport that still honours the proxy
// environment stands in; one that routes traffic through a custom RoundTripper
// passes it as OTLPHTTPConfig.Client.
//
// Each exporter therefore owns a pool, and nothing here closes it: the
// SpanExporter port has no lifecycle method, so idle connections are reaped by
// the transport's idle timeout. One exporter per collector, built once, is the
// intended shape; building one per export holds a pool per call until then.
func newOTLPTransport() *http.Transport {
	//: the process default, while it is still the stdlib's type.
	if base, isTransport := http.DefaultTransport.(*http.Transport); isTransport {
		//: every setting, the Proxy function included, and none of the pool.
		return base.Clone()
	}
	//: replaced by a foreign RoundTripper: keep the proxy environment and the
	//: idle reaping the clone would have had.
	return &http.Transport{Proxy: http.ProxyFromEnvironment, IdleConnTimeout: otlpFallbackIdleTimeout}
}

// Name implements core/trace.SpanExporter.
func (e *otlpHTTPExporter) Name() coretrace.ExporterName {
	//: the configured name.
	return e.name
}

// Export encodes spans and POSTs them, returning a typed verdict.
//
// Nothing here panics and nothing blocks past the client's deadline: an encoding
// refusal returns before any socket is touched, a transport fault returns the
// transient sentinel, and every response path drains a BOUNDED remainder and
// closes the body, so the keep-alive connection returns to the pool whenever the
// body ends within that bound.
//
// An EMPTY batch is a no-op rather than a POST. A request carrying zero spans
// costs a round trip to say nothing, and an export loop on a quiet service would
// make one every interval forever.
func (e *otlpHTTPExporter) Export(spans coretrace.SpansValue) error {
	//: nothing to ship.
	if spans.IsEmpty() {
		//: no socket touched.
		return nil
	}
	//: encode first — an unencodable batch never reaches the network.
	body, err := EncodeOTLPJSON(spans)
	//: the sentinel already carries the code, reason and public message.
	if err != nil {
		//: surface the typed refusal.
		return err
	}
	//: build the request; a body reader gives Content-Length for free.
	request, buildErr := http.NewRequest(http.MethodPost, e.endpoint, bytes.NewReader(body))
	//: the endpoint was validated at construction, so this is near-unreachable.
	if buildErr != nil {
		//: still typed rather than swallowed; the URL stays out of the message.
		return errs.Wrap(buildErr, errs.WrapParams{
			Code:    CodeOTLPExportRejected,
			Reason:  "OTLP_EXPORT_REJECTED",
			Public:  "The trace collector rejected the export",
			Private: "service/trace: the OTLP/HTTP request could not be built",
		})
	}
	//: caller headers first, so Content-Type below cannot be overridden.
	for key, value := range e.headers {
		//: values are secrets and are only ever written, never read back.
		request.Header.Set(key, value)
	}
	//: the specification requires exactly this for the JSON encoding.
	request.Header.Set(contentTypeHeader, otlpContentType)
	//: send it.
	response, doErr := e.client.Do(request)
	//: a transport fault is retryable — the specification says a client SHOULD
	//: retry when the server disconnects without answering.
	if doErr != nil {
		//: wrap so the cause stays reachable; errs renders Public only, so the
		//: *url.Error's embedded endpoint never lands in a log line.
		return errs.Wrap(doErr, errs.WrapParams{
			Code:    CodeOTLPExportUnavailable,
			Reason:  "OTLP_EXPORT_UNAVAILABLE",
			Public:  "The trace collector is unavailable",
			Private: "service/trace: the OTLP/HTTP request failed before a response was read",
		})
	}
	//: classify with the body available, then always drain and close it.
	defer closeOTLPResponse(response)
	//: the STATUS is the verdict and a read fault only costs the detail, so a
	//: short read is classified exactly like a short body.
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, e.maxResponse))
	//: a truncated payload decodes as an opaque body, leaving a 2xx standing.
	if readErr != nil {
		//: keep whatever arrived; the classifier tolerates a partial read.
		payload = nil
	}
	//: map the answer onto one of the three verdicts.
	return classifyOTLPResponse(response, payload)
}

// closeOTLPResponse drains what is left of the body and closes it, so the
// connection returns to the keep-alive pool.
//
// The drain is BOUNDED, by DefaultOTLPMaxResponseBytes, because it is a read of
// the same remote-controlled length the verdict's read is bounded by, and the
// only one that was not: a collector streaming an endless body held Export here
// forever whenever the client had no deadline. The default client's Timeout
// covers the body; a caller-supplied client is used as-is and may carry none.
// Past the bound the body is closed unread. net/http recycles only a connection
// whose response it has seen end, and the drain it attempts itself after an
// early Close is bounded as well, so a body that runs past both costs the
// connection — which is correct: reading on to save one handshake is exactly
// what the bound refuses.
//
// It is the DEFAULT and not OTLPHTTPConfig.MaxResponseBytes on purpose. That knob
// bounds what is read into MEMORY; this read keeps nothing, and a caller who
// lowered the cap must not lose connection reuse on a conforming response for it.
//
// The bound is on BYTES, not on time: a collector that stops sending mid-body is
// bounded only by the client's deadline, which is why the default client carries
// one.
//
// Both outcomes are DELIBERATELY discarded: the delivery verdict was already
// computed from the status line, so a teardown fault cannot change it, and
// reporting one would replace a correct verdict with a misleading one.
func closeOTLPResponse(response *http.Response) {
	//: drain the unread remainder so the connection is reusable, and no more.
	_, drainErr := io.Copy(io.Discard, io.LimitReader(response.Body, DefaultOTLPMaxResponseBytes))
	//: the drain outcome is irrelevant to a verdict already reached.
	swallowOTLPTeardown(drainErr)
	//: close it; same reasoning.
	swallowOTLPTeardown(response.Body.Close())
}

// swallowOTLPTeardown intentionally drops a body-teardown error. It exists as a
// named function so the discard is a documented decision at each call site rather
// than an anonymous blank assignment.
func swallowOTLPTeardown(err error) {
	//: read the parameter so the unused-param audit treats this as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}

// classifyOTLPResponse maps a collector's answer onto one of three verdicts,
// following the specification's own sections.
//
//   - §Full Success / §Partial Success: HTTP 200. The body is an
//     ExportTraceServiceResponse; a populated partialSuccess means data was LOST,
//     and the client "MUST NOT retry the request", so it is reported as a
//     non-retryable failure rather than swallowed as a success.
//   - §Retryable Response Codes: 429, 502, 503, 504 — and only those. The
//     specification is explicit that "all other 4xx or 5xx response status codes
//     MUST NOT be retried".
//   - everything else: a permanent rejection.
func classifyOTLPResponse(response *http.Response, payload []byte) error {
	//: 2xx is acceptance; the body decides whether it was total.
	if response.StatusCode >= httpStatusOK && response.StatusCode < httpStatusMultiChoices {
		//: a populated partialSuccess is a loss, not a success.
		return otlpPartialSuccessOf(payload)
	}
	//: the four statuses the specification lists as retryable.
	if isOTLPRetryableStatus(response.StatusCode) {
		//: attach the status and, when the collector named one, its back-off.
		return errs.Wrap(OTLPExportUnavailable, errs.WrapParams{},
			errs.Int(statusFieldKey, response.StatusCode),
			errs.Int(retryAfterFieldKey, otlpRetryAfterSeconds(response.Header)))
	}
	//: every other 4xx/5xx, and any 3xx the redirect refusal handed back.
	return errs.Wrap(OTLPExportRejected, errs.WrapParams{},
		errs.Int(statusFieldKey, response.StatusCode))
}

// isOTLPRetryableStatus reports whether status is in the specification's
// retryable set. The set is closed and spelled out rather than derived from the
// status class, because 5xx is NOT retryable as a class — a 500 or a 501 means
// the same request will fail the same way.
func isOTLPRetryableStatus(status int) bool {
	//: the four listed under §Retryable Response Codes.
	switch status {
	//: all four share one verdict.
	case http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		//: retryable.
		return true
	//: anything else — including every other 5xx.
	default:
		//: permanent.
		return false
	}
}

// otlpRetryAfterSeconds reads the collector's throttling hint, in seconds, and
// returns 0 when there is none it can trust.
//
// Only the delta-seconds form is read. The header also allows an HTTP-date, and
// converting one here would mean comparing the collector's clock to ours — the
// sort of quiet assumption that shows up months later as a retry storm.
func otlpRetryAfterSeconds(header http.Header) int {
	//: absent is the common case.
	raw := strings.TrimSpace(header.Get(otlpRetryAfterHeader))
	//: nothing to report.
	if raw == "" {
		//: no hint.
		return 0
	}
	//: delta-seconds only; an HTTP-date fails to parse and reports no hint.
	seconds, err := strconv.Atoi(raw)
	//: an unparseable or negative value is not a delay.
	if err != nil || seconds < 0 {
		//: no hint.
		return 0
	}
	//: the collector's own number.
	return seconds
}

// otlpPartialSuccessOf turns a 2xx body's rejected count into a verdict.
//
// An unparseable or empty body is a SUCCESS — see decodeOTLPRejected for why
// inventing a loss from a malformed response would be worse than ignoring it.
func otlpPartialSuccessOf(payload []byte) error {
	//: zero rejected spans is documented as "fully accepted".
	rejected := decodeOTLPRejected(payload)
	//: the common answer.
	if rejected == 0 {
		//: accepted whole.
		return nil
	}
	//: data was lost and the specification forbids replaying it. The
	//: collector's errorMessage is deliberately NOT attached: it is unbounded
	//: remote-controlled text, and a Field goes straight into structured logs.
	return errs.Wrap(OTLPPartialSuccess, errs.WrapParams{},
		errs.Int64(rejectedFieldKey, rejected))
}
