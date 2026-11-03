package otlpjson

import (
	"context"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
)

// discardExporter takes a document and keeps nothing.
type discardExporter struct{}

// ExportDocument discards doc.
func (discardExporter) ExportDocument(context.Context, []byte) error { return nil }

// benchRecord is a typical record: a message, three attributes, a span.
var benchRecord = corelogger.RecordEvent{
	Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Level: level.Info, Message: "request served",
	Attrs: []corelogger.AttrValue{
		{Key: "http.request.method", Value: corelogger.StringValue("GET")},
		{Key: "http.response.status_code", Value: corelogger.Int64Value(200)},
		{Key: "duration", Value: corelogger.DurationValue(12 * time.Millisecond)},
	},
	TraceContext: corelogger.TraceContextValue{TraceID: [corelogger.TraceIDLen]byte{1, 2, 3}, SpanID: [corelogger.SpanIDLen]byte{4, 5}},
}

// BenchmarkSink_OTLP_Batch16 is the sink's WriteBatch of 16 records — one
// document — and reports the cost per record (#867): the encoding into the
// reused buffer, the export a no-op.
func BenchmarkSink_OTLP_Batch16(b *testing.B) {
	s := newSink(&Config{Exporter: discardExporter{}, Resource: coreotel.ResourceValue{Attrs: []coreotel.AttrValue{coreotel.String("service.name", "orders")}}})
	recs := make([]corelogger.RecordEvent, 16)
	for i := range recs {
		recs[i] = benchRecord
	}
	ctx := context.Background()
	_, _ = s.WriteBatch(ctx, recs, nil)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = s.WriteBatch(ctx, recs, nil)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(recs)), "ns/record")
}

// BenchmarkSink_OTLP_Write is one record per document, as the drainer hands
// a lone record.
func BenchmarkSink_OTLP_Write(b *testing.B) {
	s := newSink(&Config{Exporter: discardExporter{}})
	ctx := context.Background()
	_, _ = s.Write(ctx, benchRecord, nil)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = s.Write(ctx, benchRecord, nil)
	}
}
