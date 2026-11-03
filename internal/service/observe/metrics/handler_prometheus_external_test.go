package metrics_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// scrape runs one request through h.
func scrape(h http.Handler, method string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, "/metrics", nil))
	return rec
}

// TestPrometheusHandlerServesTheExportersBytes (#1020): a GET over a
// cumulative meter answers 200, PrometheusContentType, a Content-Length equal
// to the body, and the body the exporter writes for the same snapshot.
func TestPrometheusHandlerServesTheExportersBytes(t *testing.T) {
	t.Parallel()
	m := metrics.NewMeter()
	m.Counter("requests_total").Add(3)
	h, err := metrics.NewPrometheusHandler(m.Collect, metrics.PrometheusHandlerConfig{})
	if err != nil {
		t.Fatalf("NewPrometheusHandler: %v", err)
	}
	rec := scrape(h, http.MethodGet)
	var want bytes.Buffer
	if err := metrics.NewPrometheusExporter("prometheus", &want).Export(m.Collect()); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != metrics.PrometheusContentType {
		t.Fatalf("status %d, content type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) || rec.Body.String() != want.String() {
		t.Fatalf("Content-Length %s for %d bytes; body\n%s\nwant\n%s", rec.Header().Get("Content-Length"), rec.Body.Len(), rec.Body, &want)
	}
}

// TestPrometheusHandlerAnswers500ForARefusedDocument: a meter holding the
// name a.b answers 500 with INVALID_METRIC_NAME's Public text and no sample.
func TestPrometheusHandlerAnswers500ForARefusedDocument(t *testing.T) {
	t.Parallel()
	m := metrics.NewMeter()
	m.Counter("a.b").Add(1)
	h, _ := metrics.NewPrometheusHandler(m.Collect, metrics.PrometheusHandlerConfig{})
	rec := scrape(h, http.MethodGet)
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != coremetrics.InvalidMetricName.Public() {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "a.b") {
		t.Fatal("a refused document leaked into the response")
	}
}

// TestPrometheusHandlerMethodsAndOneScrapeAtATime: POST is 405 with Allow,
// HEAD is 200 with headers and no body, and a GET while another runs is 503
// with Retry-After.
func TestPrometheusHandlerMethodsAndOneScrapeAtATime(t *testing.T) {
	t.Parallel()
	m := metrics.NewMeterWithConfig(metrics.MeterConfig{Temporality: coremetrics.TemporalityDelta})
	inside, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h, _ := metrics.NewPrometheusHandler(func() coremetrics.SnapshotValue {
		once.Do(func() {
			close(inside)
			<-release
		})
		return m.Collect()
	}, metrics.PrometheusHandlerConfig{})
	if rec := scrape(h, http.MethodPost); rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST: %d, Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
	if rec := scrape(h, http.MethodHead); rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != metrics.PrometheusContentType {
		t.Fatalf("HEAD: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- scrape(h, http.MethodGet) }()
	<-inside
	second := scrape(h, http.MethodGet)
	close(release)
	if second.Code != http.StatusServiceUnavailable || second.Header().Get("Retry-After") != "1" {
		t.Fatalf("concurrent GET: %d, Retry-After %q", second.Code, second.Header().Get("Retry-After"))
	}
	if rec := <-first; rec.Code != http.StatusOK {
		t.Fatalf("first GET: %d", rec.Code)
	}
}

// TestPrometheusHandlerRefusesANilCollect: CollectorNil, 0.3.45.11.
func TestPrometheusHandlerRefusesANilCollect(t *testing.T) {
	t.Parallel()
	if _, err := metrics.NewPrometheusHandler(nil, metrics.PrometheusHandlerConfig{}); !errs.HasCode(err, coremetrics.CodeCollectorNil) {
		t.Fatalf("nil collect: %v", err)
	}
}
