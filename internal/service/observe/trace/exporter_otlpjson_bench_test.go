// Package trace_test — what one OTLP/JSON document costs to encode from a
// batch: the tree built from the spans, then rendered. The batch is built once,
// outside the measurement, the way a sink hands one to an exporter.
package trace_test

import (
	"encoding/binary"
	"testing"
	"time"

	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	svctrace "github.com/kitsunium/sdk/internal/service/observe/trace"
)

// benchOTLPSpanCount is a full default batch: the sink's MaxExportBatchSize.
const benchOTLPSpanCount int = 512

// benchOTLPDoc keeps the compiler from proving a document dead.
var benchOTLPDoc []byte

// benchOTLPBatch is benchOTLPSpanCount server spans of one trace, each the
// child of a remote parent, with four attributes; one in ten carries an error
// status and an exception event.
func benchOTLPBatch() coretrace.SpansValue {
	start := time.Unix(1_700_000_000, 0)
	var traceID coretrace.TraceID
	binary.BigEndian.PutUint64(traceID[:8], 0x4bf92f3577b34da6)
	binary.BigEndian.PutUint64(traceID[8:], 0xa3ce929d0e0e4736)
	parent := coretrace.SpanContextValue{TraceID: traceID, SpanID: coretrace.SpanID{1, 2, 3, 4, 5, 6, 7, 8}, Flags: coretrace.FlagSampled, Remote: true}
	spans := make([]coretrace.SpanValue, benchOTLPSpanCount)
	for i := range spans {
		var spanID coretrace.SpanID
		binary.BigEndian.PutUint64(spanID[:], uint64(i)+0x00f067aa0ba902b7)
		span := coretrace.SpanValue{
			Context:   coretrace.SpanContextValue{TraceID: traceID, SpanID: spanID, Flags: coretrace.FlagSampled},
			Parent:    parent,
			Name:      "GET /v1/orders/{id}",
			Kind:      coretrace.SpanKindServer,
			StartTime: start.Add(time.Duration(i) * time.Millisecond),
			EndTime:   start.Add(time.Duration(i)*time.Millisecond + 1_234_567),
			Attrs: coretrace.SortAttrs([]coreotel.AttrValue{
				coreotel.String("http.request.method", "GET"),
				coreotel.String("http.route", "/v1/orders/{id}"),
				coreotel.Int64("http.response.status_code", 200),
				coreotel.Float64("sample.ratio", 0.25),
			}),
		}
		if i%10 == 0 {
			span.Status = coretrace.StatusValue{Code: coretrace.StatusError, Message: "upstream refused"}
			span.Events = []coretrace.EventValue{{
				Time:  span.EndTime,
				Name:  coretrace.ExceptionEventName,
				Attrs: []coreotel.AttrValue{coreotel.String(coretrace.ExceptionMessageKey, "upstream refused")},
			}}
		}
		spans[i] = span
	}
	return coretrace.SpansValue{
		Resource: coreotel.ResourceValue{Attrs: []coreotel.AttrValue{coreotel.String(coreotel.ServiceNameKey, "checkout")}},
		Scope:    coreotel.ScopeValue{Name: coretrace.DefaultScopeName, Version: "9.9.9"},
		Spans:    spans,
	}
}

// BenchmarkEncodeOTLPJSON_512Spans prices one full default batch.
func BenchmarkEncodeOTLPJSON_512Spans(b *testing.B) {
	batch := benchOTLPBatch()
	b.ReportAllocs()
	for b.Loop() {
		doc, err := svctrace.EncodeOTLPJSON(batch)
		if err != nil {
			b.Fatal(err)
		}
		benchOTLPDoc = doc
	}
}
