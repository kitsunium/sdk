package otlpjson_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/core/observe/logger/writer"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/logger/writer/otlpjson"
)

// at is the instant both records are stamped with: 1767323045 s.
var at = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// otlpLogsGolden is the hand-written ExportLogsServiceRequest of two
// records, assembled from logs.proto one message at a time:
// ExportLogsServiceRequest.resource_logs (1) → ResourceLogs.resource (1),
// scope_logs (2) → ScopeLogs.scope (1), log_records (2); each LogRecord in
// field-number order — time_unix_nano (1, fixed64 → decimal string),
// severity_number (2, an enum → integer), severity_text (3), body (5, an
// AnyValue), attributes (6, omitted when none), trace_id (9) and span_id
// (10), hex, present only for the record emitted inside a span.
const otlpLogsGolden = `{"resourceLogs":[{` +
	`"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"orders"}}]},` +
	`"scopeLogs":[{"scope":{"name":"github.com/acme/orders"},"logRecords":[` +
	`{"timeUnixNano":"1767323045000000000","severityNumber":13,"severityText":"WARN",` +
	`"body":{"stringValue":"charge declined"},` +
	`"attributes":[` +
	`{"key":"attempt","value":{"intValue":"3"}},` +
	`{"key":"http.request.latency","value":{"intValue":"1500000"}},` +
	`{"key":"http.status","value":{"intValue":"402"}},` +
	`{"key":"retry","value":{"boolValue":true}},` +
	`{"key":"ratio","value":{"doubleValue":0.25}},` +
	`{"key":"big","value":{"stringValue":"18446744073709551615"}}` +
	`],` +
	`"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"00f067aa0ba902b7"},` +
	`{"timeUnixNano":"1767323045000000000","severityNumber":9,"severityText":"INFO",` +
	`"body":{"stringValue":"outside any span"}}` +
	`]}]}]}`

// TestEncodeOTLPJSONWritesTheHandWrittenDocument (#867 acceptance): the
// encoder's bytes for a record inside a span and one outside equal the
// hand-written document, field order included — the record outside carries
// no trace identifiers.
func TestEncodeOTLPJSONWritesTheHandWrittenDocument(t *testing.T) {
	t.Parallel()
	inside := corelogger.RecordEvent{
		Time: at, Level: level.Warn, Message: "charge declined",
		Attrs: []corelogger.AttrValue{
			{Key: "attempt", Value: corelogger.Int64Value(3)},
			{Key: "http", Value: corelogger.GroupValue(
				corelogger.AttrValue{Key: "request.latency", Value: corelogger.DurationValue(1500 * time.Microsecond)},
				corelogger.AttrValue{Key: "status", Value: corelogger.Int64Value(402)},
			)},
			{Key: "retry", Value: corelogger.BoolValue(true)},
			{Key: "ratio", Value: corelogger.Float64Value(0.25)},
			{Key: "big", Value: corelogger.Uint64Value(1<<64 - 1)},
		},
		TraceContext: corelogger.TraceContextValue{
			TraceID: [corelogger.TraceIDLen]byte{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36},
			SpanID:  [corelogger.SpanIDLen]byte{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7},
		},
	}
	outside := corelogger.RecordEvent{Time: at, Level: level.Info, Message: "outside any span"}
	resource := coreotel.ResourceValue{Attrs: []coreotel.AttrValue{coreotel.String("service.name", "orders")}}
	scope := coreotel.ScopeValue{Name: "github.com/acme/orders"}
	got := string(otlpjson.EncodeOTLPJSON(resource, scope, []corelogger.RecordEvent{inside, outside}))
	if got != otlpLogsGolden {
		t.Fatalf("OTLP/JSON logs:\n got %s\nwant %s", got, otlpLogsGolden)
	}
}

// TestSeverityFollowsTheSlogBridge: the level+9 mapping, held in 1…24.
func TestSeverityFollowsTheSlogBridge(t *testing.T) {
	t.Parallel()
	for lv, want := range map[level.Level]string{
		level.Debug: `"severityNumber":5,`, level.Info: `"severityNumber":9,`,
		level.Warn: `"severityNumber":13,`, level.Error: `"severityNumber":17,`,
		level.Level(-100): `"severityNumber":1,`, level.Level(100): `"severityNumber":24,`,
	} {
		doc := string(otlpjson.EncodeOTLPJSON(coreotel.ResourceValue{}, coreotel.ScopeValue{}, []corelogger.RecordEvent{{Time: at, Level: lv}}))
		if !contains(doc, want) {
			t.Errorf("level %d: %s lacks %s", lv, doc, want)
		}
	}
}

// contains is strings.Contains without the import.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestOpenRefusesAConfigWithNoExporter: the writer needs somewhere to send
// its documents; a wrong config type is refused too.
func TestOpenRefusesAConfigWithNoExporter(t *testing.T) {
	t.Parallel()
	if _, err := otlpjson.Writer.Open(otlpjson.Config{}); !errs.HasCode(err, writer.CodeWriterConfigInvalid) {
		t.Fatalf("Open with no Exporter = %v, want WRITER_CONFIG_INVALID", err)
	}
	if _, err := otlpjson.Writer.Open(struct{}{}); !errors.Is(err, writer.WriterConfigInvalid) {
		t.Fatalf("Open with a foreign config = %v", err)
	}
	if otlpjson.Writer.Name() != "otlpjson" {
		t.Fatalf("Name = %q", otlpjson.Writer.Name())
	}
}

// recordingExporter keeps every document it is handed.
type recordingExporter struct {
	docs chan string
	err  error
}

// ExportDocument records doc.
func (e *recordingExporter) ExportDocument(_ context.Context, doc []byte) error {
	e.docs <- string(doc)
	return e.err
}

// TestABacklogIsOneDocument: records the drainer holds leave as one
// document, and a refused document fails each of its records once.
func TestABacklogIsOneDocument(t *testing.T) {
	t.Parallel()
	refused := errors.New("collector down")
	exp := &recordingExporter{docs: make(chan string, 8), err: refused}
	var failures atomic.Int64
	sink, err := otlpjson.Writer.Open(otlpjson.Config{Exporter: exp, OnError: func(error) { failures.Add(1) }})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for range 3 {
		if _, err := sink.Write(context.Background(), corelogger.RecordEvent{Time: at, Message: "m"}, []byte("m\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	var records int
	for len(exp.docs) > 0 {
		records += count(<-exp.docs, `"timeUnixNano"`)
	}
	if records != 3 || failures.Load() != 3 {
		t.Fatalf("%d records exported, %d failures reported; want 3 and 3", records, failures.Load())
	}
}

// count counts sub in s.
func count(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}
