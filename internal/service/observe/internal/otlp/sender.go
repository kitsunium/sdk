// Package otlp — the OTLP/HTTP sender: the only file of this package, and of
// the two signals that use it, that opens an outbound socket.
package otlp

import (
	"bytes"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fallbackIdleTimeout is how long the default client's fallback transport
// keeps an idle connection — see newTransport. It is net/http's own
// DefaultTransport value, so the fallback reaps its pool exactly as the clone
// it stands in for does, rather than holding a connection for as long as the
// collector tolerates it.
const fallbackIdleTimeout time.Duration = 90 * time.Second

// contentType labels the body. The specification is explicit that both client
// and server MUST set "Content-Type: application/json" for the JSON encoding,
// so the sender sets it LAST and it always wins over a caller-supplied header
// of the same name.
const contentType string = "application/json"

// The two header names the sender writes and reads. Retry-After carries the
// collector's own back-off hint under §OTLP/HTTP Throttling; the client SHOULD
// honour it — which is the caller's job here, because nothing here retries.
const (
	contentTypeHeader string = "Content-Type"
	retryAfterHeader  string = "Retry-After"
)

// The 2xx window, named so the classification reads as the specification does.
const (
	httpStatusOK           int = 200
	httpStatusMultiChoices int = 300
)

// The two errs Field keys every verdict may carry; the third, the rejected
// count's, is the signal's (SignalSpec.RejectedField). None of them carries
// remote-controlled text or the endpoint.
const (
	retryAfterFieldKey string = "retry_after_seconds"
	statusFieldKey     string = "http_status"
)

// The two URL schemes OTLP/HTTP speaks, plus the path that is not one.
const (
	schemeHTTP  string = "http"
	schemeHTTPS string = "https"
	rootPath    string = "/"
)

// Sender POSTs encoded OTLP/JSON documents to one collector and maps each
// answer onto one of three verdicts, under the calling signal's codes.
//
// It holds no mutable state, so it needs no mutex: http.Client is safe for
// concurrent use, and every request builds its own body reader from its own
// document.
//
// It does NOT retry, and that is a decision rather than an omission. The
// specification asks a client to honour Retry-After and otherwise back off
// exponentially; internal/service/resilience already ships that policy, and a
// backoff hidden inside a send would be a second one a caller cannot see, tune
// or cancel — an exporter's Export has no context to cancel it with. What it
// supplies instead is the CLASSIFICATION a retry policy needs: a transient
// verdict carries the signal's Unavailable code, which each signal's
// OTLPRetryable reads.
type Sender struct {
	// endpoint is the validated full URL.
	endpoint string
	// client is the caller's, or the bounded redirect-refusing default.
	client *http.Client
	// headers are owned copies of the caller's, written on every request.
	headers map[string]string
	// maxResponse caps the response body read.
	maxResponse int64
	// signal names every verdict in the calling signal's words.
	signal *SignalSpec
}

// NewSender validates cfg and builds the sender a signal's
// NewOTLPHTTPExporter wraps. A refused endpoint fails HERE, at wiring, under
// the signal's EndpointInvalid code, rather than at the first export: an
// endpoint is STRUCTURE, wrong on the first export or never, which is what
// makes refusing it safe.
//
// cfg is taken by pointer so the configuration is not copied onto the stack;
// nothing in it is retained except owned copies.
func NewSender(cfg *HTTPConfig, signal *SignalSpec) (sender *Sender, err error) {
	//: an endpoint that cannot be a collector fails here, not in production.
	if endpointErr := checkEndpoint(cfg.Endpoint, signal); endpointErr != nil {
		//: surface the typed refusal; nothing is constructed.
		return nil, endpointErr
	}
	//: clamp the two bounded knobs; neither has an inert setting.
	timeout := cfg.Timeout
	//: non-positive takes the specification's own default.
	if timeout <= 0 {
		//: OTEL_EXPORTER_OTLP_TIMEOUT's documented default.
		timeout = DefaultTimeout
	}
	//: same clamp for the response cap.
	maxResponse := cfg.MaxResponseBytes
	//: non-positive takes the default; "unbounded" is not offered.
	if maxResponse <= 0 {
		//: generous next to a conforming response, finite next to a hostile one.
		maxResponse = DefaultMaxResponseBytes
	}
	//: a caller-supplied client is used as-is, deadline and redirects included.
	client := cfg.Client
	//: only build one when the caller brought none.
	if client == nil {
		//: bounded and redirect-refusing — see newClient.
		client = newClient(timeout)
	}
	//: own the header map so a later caller mutation cannot change the wire.
	return &Sender{
		endpoint:    cfg.Endpoint,
		client:      client,
		headers:     maps.Clone(cfg.Headers),
		maxResponse: maxResponse,
		signal:      signal,
	}, nil
}

// checkEndpoint refuses an endpoint that cannot address a collector.
//
// The path check is the one that earns its keep: every other mistake surfaces
// as a connection error a reader recognises, while a bare
// "http://collector:4318" connects, gets a 404, and looks exactly like a
// collector that is up.
func checkEndpoint(endpoint string, signal *SignalSpec) error {
	//: an empty endpoint has no failure mode worth distinguishing.
	if endpoint == "" {
		//: refuse without echoing anything.
		return signal.EndpointInvalid
	}
	//: parse strictly — a relative reference has no host to POST to.
	parsed, err := url.Parse(endpoint)
	//: a malformed URL is refused with its cause on the trail, never its text.
	if err != nil {
		//: errs renders the Public message only, so the URL stays out of logs.
		return errs.Wrap(err, signal.EndpointUnparsable)
	}
	//: three refusals with one effect: OTLP/HTTP is http or https, a host is
	//: what there is to connect to, and the signal path must be spelled
	//: because an endpoint is used as-is.
	if (parsed.Scheme != schemeHTTP && parsed.Scheme != schemeHTTPS) ||
		parsed.Host == "" ||
		parsed.Path == "" || parsed.Path == rootPath {
		//: refuse without echoing the endpoint.
		return signal.EndpointInvalid
	}
	//: the endpoint can address a collector.
	return nil
}

// newClient builds the default client: bounded by timeout, refusing to follow
// a redirect, and riding a connection pool of its own.
//
// The redirect refusal is CWE-918. A 30x from a collector — or from anything
// sitting in front of one — would otherwise bounce a POST carrying the
// caller's Authorization header to whatever host the response names, past an
// allowlist that only ever saw the configured endpoint. ErrUseLastResponse
// stops the chain and hands the 30x back as-is, where it is classified as a
// permanent rejection.
//
// The pool is newTransport's, which says why it is not the process's.
func newClient(timeout time.Duration) *http.Client {
	//: Timeout covers dial, write, read and body — the whole round trip.
	return &http.Client{
		Timeout: timeout,
		//: this sender's own pool, which nothing else in the process can empty.
		Transport: newTransport(),
		//: stop at the first redirect and return that response.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			//: the stdlib signal for "hand it back", not an error verdict.
			return http.ErrUseLastResponse
		},
	}
}

// newTransport returns the connection pool the default client owns: a clone of
// http.DefaultTransport, so every setting a process expects of an outbound
// client — the proxy environment first of all — carries over, and none of its
// connections do.
//
// # Why the default client does not share http.DefaultTransport
//
// Because anything in the process can empty that pool, and net/http turns
// doing so at the wrong instant into a wrong verdict. A response with no body
// is put back in the idle pool BEFORE the waiting round trip is handed it, and
// a CloseIdleConnections landing in that window closes the connection under
// it: the round trip then reports "connection broken" for a response that had
// already arrived. That made a 200 a retryable Unavailable — the caller's retry
// replays data the collector accepted, which double-counts a delta point or
// stores a span twice — a 429 lose its Retry-After, and a 413 retryable. Every
// httptest.Server.Close and every http.DefaultClient.CloseIdleConnections is
// such a call, made by code this sender cannot see.
//
// A caller-supplied HTTPConfig.Client is used as given, Transport included,
// so one that rides http.DefaultTransport inherits that exposure; giving it a
// Transport of its own is how it avoids it.
//
// The clone is a snapshot taken at construction. A process that has replaced
// http.DefaultTransport with something other than an *http.Transport leaves
// nothing to clone, and a fresh transport that still honours the proxy
// environment stands in; one that routes traffic through a custom
// RoundTripper passes it as HTTPConfig.Client.
//
// Each sender therefore owns a pool, and nothing here closes it: an exporter
// port has no lifecycle method, so idle connections are reaped by the
// transport's idle timeout. One exporter per collector, built once, is the
// intended shape; building one per export holds a pool per call until then.
func newTransport() *http.Transport {
	//: the process default, while it is still the stdlib's type.
	if base, isTransport := http.DefaultTransport.(*http.Transport); isTransport {
		//: every setting, the Proxy function included, and none of the pool.
		return base.Clone()
	}
	//: replaced by a foreign RoundTripper: keep the proxy environment and the
	//: idle reaping the clone would have had.
	return &http.Transport{Proxy: http.ProxyFromEnvironment, IdleConnTimeout: fallbackIdleTimeout}
}

// Post sends body — one document Marshal returned — and returns a typed
// verdict under the signal's codes.
//
// Nothing here panics and nothing blocks past the client's deadline: a
// transport fault returns the transient verdict, and every response path
// drains a BOUNDED remainder and closes the body, so the keep-alive connection
// returns to the pool whenever the body ends within that bound.
func (s *Sender) Post(body []byte) error {
	//: build the request; a body reader gives Content-Length for free.
	request, buildErr := http.NewRequest(http.MethodPost, s.endpoint, bytes.NewReader(body))
	//: the endpoint was validated at construction, so this is near-unreachable.
	if buildErr != nil {
		//: still typed rather than swallowed; the URL stays out of the message.
		return errs.Wrap(buildErr, s.signal.RequestUnbuildable)
	}
	//: caller headers first, so Content-Type below cannot be overridden.
	for key, value := range s.headers {
		//: values are secrets and are only ever written, never read back.
		request.Header.Set(key, value)
	}
	//: the specification requires exactly this for the JSON encoding.
	request.Header.Set(contentTypeHeader, contentType)
	//: send it.
	response, doErr := s.client.Do(request)
	//: a transport fault is retryable — the specification says a client SHOULD
	//: retry when the server disconnects without answering.
	if doErr != nil {
		//: wrap so the cause stays reachable; errs renders Public only, so the
		//: *url.Error's embedded endpoint never lands in a log line.
		return errs.Wrap(doErr, s.signal.TransportFault)
	}
	//: classify with the body available, then always drain and close it.
	defer closeResponse(response)
	//: the STATUS is the verdict and a read fault only costs the detail, so a
	//: short read is classified exactly like a short body.
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, s.maxResponse))
	//: a truncated payload decodes as an opaque body, which leaves a 2xx standing.
	if readErr != nil {
		//: keep nothing; classify tolerates a missing body.
		payload = nil
	}
	//: map the answer onto one of the three verdicts.
	return s.classify(response, payload)
}

// closeResponse drains what is left of the body and closes it, so the
// connection returns to the keep-alive pool.
//
// The drain is BOUNDED, by DefaultMaxResponseBytes, because it is a read of the
// same remote-controlled length the verdict's read is bounded by, and the only
// one that was not: a collector streaming an endless body held an export here
// forever whenever the client had no deadline. The default client's Timeout
// covers the body; a caller-supplied client is used as-is and may carry none.
// Past the bound the body is closed unread. net/http recycles only a
// connection whose response it has seen end, and the drain it attempts itself
// after an early Close is bounded as well, so a body that runs past both costs
// the connection — which is correct: reading on to save one handshake is
// exactly what the bound refuses.
//
// It is the DEFAULT and not HTTPConfig.MaxResponseBytes on purpose. That knob
// bounds what is read into MEMORY; this read keeps nothing, and a caller who
// lowered the cap must not lose connection reuse on a conforming response for
// it.
//
// The bound is on BYTES, not on time: a collector that stops sending mid-body
// is bounded only by the client's deadline, which is why the default client
// carries one.
//
// Both outcomes are DELIBERATELY discarded: the delivery verdict was already
// computed from the status line, so a teardown fault cannot change it, and
// reporting one would replace a correct verdict with a misleading one. Same
// posture as the nettransport writer's swallowErr.
func closeResponse(response *http.Response) {
	//: drain the unread remainder so the connection is reusable, and no more.
	_, drainErr := io.Copy(io.Discard, io.LimitReader(response.Body, DefaultMaxResponseBytes))
	//: the drain outcome is irrelevant to a verdict already reached.
	swallowTeardown(drainErr)
	//: close it; same reasoning.
	swallowTeardown(response.Body.Close())
}

// swallowTeardown intentionally drops a body-teardown error. It exists as a
// named function so the discard is a documented decision at each call site
// rather than an anonymous blank assignment.
func swallowTeardown(err error) {
	//: read the parameter so the unused-param audit treats this as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}

// classify maps a collector's answer onto one of three verdicts, following the
// specification's own sections.
//
//   - §Full Success / §Partial Success: HTTP 2xx. The body is the signal's
//     ExportXServiceResponse; a populated partialSuccess means data was LOST,
//     and the client "MUST NOT retry the request", so it is reported as a
//     non-retryable failure rather than swallowed as a success.
//   - §Retryable Response Codes: 429, 502, 503, 504 — and only those. The
//     specification is explicit that "all other 4xx or 5xx response status
//     codes MUST NOT be retried".
//   - everything else: a permanent rejection.
func (s *Sender) classify(response *http.Response, payload []byte) error {
	//: 2xx is acceptance; the body decides whether it was total.
	if response.StatusCode >= httpStatusOK && response.StatusCode < httpStatusMultiChoices {
		//: a populated partialSuccess is a loss, not a success.
		return s.partialSuccessOf(payload)
	}
	//: the four statuses the specification lists as retryable.
	if isRetryableStatus(response.StatusCode) {
		//: attach the status and, when the collector named one, its back-off.
		return errs.Wrap(s.signal.Unavailable, errs.WrapParams{},
			errs.Int(statusFieldKey, response.StatusCode),
			errs.Int(retryAfterFieldKey, retryAfterSeconds(response.Header)))
	}
	//: every other 4xx/5xx, and any 3xx the redirect refusal handed back.
	return errs.Wrap(s.signal.Rejected, errs.WrapParams{},
		errs.Int(statusFieldKey, response.StatusCode))
}

// isRetryableStatus reports whether status is in the specification's
// retryable set. The set is closed and spelled out rather than derived from the
// status class, because 5xx is NOT retryable as a class — a 500 or a 501 means
// the same request will fail the same way.
func isRetryableStatus(status int) bool {
	//: the four listed under §Retryable Response Codes — too many requests,
	//: a broken gateway, a collector shedding load, and a gateway that gave up.
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

// retryAfterSeconds reads the collector's throttling hint, in seconds, and
// returns 0 when there is none it can trust.
//
// Only the delta-seconds form is read. The header also allows an HTTP-date,
// and converting one here would mean comparing the collector's clock to ours —
// the sort of quiet assumption that shows up months later as a retry storm. A
// caller who needs the date form reads the header from their own http.Client's
// response.
func retryAfterSeconds(header http.Header) int {
	//: absent is the common case.
	raw := strings.TrimSpace(header.Get(retryAfterHeader))
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

// partialSuccessOf turns a 2xx body's rejected count into a verdict.
//
// An unparseable or empty body is a SUCCESS — see DecodeRejected for why
// inventing a loss from a malformed response would be worse than ignoring it.
func (s *Sender) partialSuccessOf(payload []byte) error {
	//: zero rejected items is documented as "fully accepted".
	rejected := s.signal.DecodeRejected(payload)
	//: the common answer.
	if rejected == 0 {
		//: accepted whole.
		return nil
	}
	//: data was lost and the specification forbids replaying it. The
	//: collector's errorMessage is deliberately NOT attached: it is unbounded
	//: remote-controlled text, and a Field goes straight into structured logs.
	return errs.Wrap(s.signal.PartialSuccess, errs.WrapParams{},
		errs.Int64(s.signal.RejectedField, rejected))
}
