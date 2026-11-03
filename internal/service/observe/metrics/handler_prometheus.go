package metrics

import (
	"bytes"
	"net/http"
	"strconv"
	"sync"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// PrometheusContentType is the Content-Type of the Prometheus text exposition
// format, version 0.0.4 — what NewPrometheusHandler answers a scrape with.
const PrometheusContentType string = "text/plain; version=0.0.4; charset=utf-8"

// The handler's fixed answers.
const (
	// defaultHandlerExporter names the exporter when the config names none.
	defaultHandlerExporter coremetrics.ExporterName = "prometheus"
	// allowedMethods is the Allow header of a 405.
	allowedMethods string = "GET, HEAD"
	// busyRetryAfter is the Retry-After of a 503, in seconds.
	busyRetryAfter string = "1"
	// errorContentType labels a 500's body, the refusal's Public text.
	errorContentType string = "text/plain; charset=utf-8"
	// maxPooledDocument bounds the buffers the handler keeps between scrapes.
	maxPooledDocument int = 4 << 20
)

// PrometheusHandlerConfig tunes NewPrometheusHandler. The zero value is
// usable: the exporter is named "prometheus".
type PrometheusHandlerConfig struct {
	// Name is the exporter name the handler renders under; empty is "prometheus".
	Name coremetrics.ExporterName
	// OnError, when non-nil, is called with a failed write of a response — a
	// scraper that hung up mid-document, the one failure left once the status
	// line is out. nil drops it: the handler has nowhere else to report it.
	OnError func(err error)
}

// prometheusHandler is the /metrics endpoint NewPrometheusHandler builds.
type prometheusHandler struct {
	// collect is the meter's Collect, bound.
	collect func() coremetrics.SnapshotValue
	// name is the exporter name the document is rendered under.
	name coremetrics.ExporterName
	// onError receives a failed response write; nil drops it.
	onError func(err error)
	// scrape admits one scrape at a time; TryLock, never Lock: a second scrape
	// answers 503 rather than waiting to split a delta window.
	scrape sync.Mutex
	// docs recycles the document buffers between scrapes.
	docs sync.Pool
}

// NewPrometheusHandler returns the /metrics scrape endpoint over collect — the
// meter's Collect, bound. It answers GET with the snapshot rendered by the
// Prometheus exporter into a buffer and written once, with
// PrometheusContentType and a Content-Length, so a refused document
// (INVALID_METRIC_NAME, RESERVED_LABEL_NAME, UNSUPPORTED_TEMPORALITY) writes
// nothing of itself and answers 500 with the refusal's Public text alone —
// never a 200 with an empty body, which a scraper reads as every series gone.
// HEAD answers the headers without collecting, so a probe never consumes a
// delta window; any other method is 405 with Allow: GET, HEAD. One scrape
// runs at a time: a concurrent one answers 503 with Retry-After: 1, so a delta
// meter keeps one reader. An Accept for OpenMetrics is answered in 0.0.4, the
// one format the connector writes. A nil collect is refused with CollectorNil.
func NewPrometheusHandler(collect func() coremetrics.SnapshotValue, cfg PrometheusHandlerConfig) (handler http.Handler, err error) {
	//: a scrape endpoint with nothing behind it.
	if collect == nil {
		return nil, coremetrics.CollectorNil
	}
	name := cfg.Name
	//: the zero config names the exporter after its format.
	if name == "" {
		name = defaultHandlerExporter
	}
	h := &prometheusHandler{collect: collect, name: name, onError: cfg.OnError}
	h.docs.New = func() any { return new(bytes.Buffer) }
	return h, nil
}

// ServeHTTP answers one scrape.
func (h *prometheusHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.serveGet(w)
	case http.MethodHead:
		//: the headers a GET would carry, and no collection behind them.
		w.Header().Set("Content-Type", PrometheusContentType)
		w.WriteHeader(http.StatusOK)
	default:
		w.Header().Set("Allow", allowedMethods)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

// serveGet collects and renders one document, or answers 503 while another
// scrape runs, or 500 with the refusal's Public text.
func (h *prometheusHandler) serveGet(w http.ResponseWriter) {
	//: one reader of a delta meter at a time.
	if !h.scrape.TryLock() {
		w.Header().Set("Retry-After", busyRetryAfter)
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	defer h.scrape.Unlock()
	doc, _ := h.docs.Get().(*bytes.Buffer)
	doc.Reset()
	defer h.recycle(doc)
	//: rendered whole before a byte leaves, so a refusal leaves nothing behind.
	if eerr := NewPrometheusExporter(h.name, doc).Export(h.collect()); eerr != nil {
		w.Header().Set("Content-Type", errorContentType)
		w.WriteHeader(http.StatusInternalServerError)
		h.write(w, []byte(publicText(eerr)))
		return
	}
	w.Header().Set("Content-Type", PrometheusContentType)
	w.Header().Set("Content-Length", strconv.Itoa(doc.Len()))
	w.WriteHeader(http.StatusOK)
	h.write(w, doc.Bytes())
}

// write sends body and hands a failure — the scraper gone — to OnError.
func (h *prometheusHandler) write(w http.ResponseWriter, body []byte) {
	//: the status line is out: the caller's hook is the only report left.
	if _, werr := w.Write(body); werr != nil && h.onError != nil {
		h.onError(werr)
	}
}

// recycle returns doc to the pool unless a large scrape grew it too far.
func (h *prometheusHandler) recycle(doc *bytes.Buffer) {
	//: a one-off huge document is not kept for the process's lifetime.
	if doc.Cap() > maxPooledDocument {
		return
	}
	h.docs.Put(doc)
}

// publicText is err's wire-safe Public text — never Private, never the
// fields — or a generic one for an error that is not the SDK's.
func publicText(err error) string {
	//: an SDK refusal carries its own wire-safe sentence.
	if public := errs.PublicOf(err); public != "" {
		return public
	}
	//: anything else says only what happened to the scrape.
	return http.StatusText(http.StatusInternalServerError)
}
