package metrics_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	"github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// BenchmarkHandler_1000Series prices one scrape of a meter holding 1000
// counter series through NewPrometheusHandler: Collect, the render into the
// pooled buffer, and the write (#1020).
func BenchmarkHandler_1000Series(b *testing.B) {
	m := metrics.NewMeter()
	//: 50 names × 20 routes: 1000 series.
	for i := range 1000 {
		m.Counter("requests_total_"+strconv.Itoa(i%50), coreotel.String("route", strconv.Itoa(i/50))).Add(int64(i))
	}
	h, err := metrics.NewPrometheusHandler(m.Collect, metrics.PrometheusHandlerConfig{})
	if err != nil {
		b.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	b.ReportAllocs()
	for b.Loop() {
		rec.Body.Reset()
		h.ServeHTTP(rec, req)
	}
}

// BenchmarkHandler_1000Series_ExportOnly is the same snapshot collected and
// rendered into a buffer with no handler: the floor the handler adds to.
func BenchmarkHandler_1000Series_ExportOnly(b *testing.B) {
	m := metrics.NewMeter()
	for i := range 1000 {
		m.Counter("requests_total_"+strconv.Itoa(i%50), coreotel.String("route", strconv.Itoa(i/50))).Add(int64(i))
	}
	var buf bytes.Buffer
	b.ReportAllocs()
	for b.Loop() {
		buf.Reset()
		_ = metrics.NewPrometheusExporter("prometheus", &buf).Export(m.Collect())
	}
}
