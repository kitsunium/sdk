package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/logger"
	"github.com/kitsunium/sdk/pkg/v1/trace"
)

// The W3C Trace Context specification's own traceparent example.
const (
	fixtureTraceHex string = "4bf92f3577b34da6a3ce929d0e0e4736"
	fixtureSpanHex  string = "00f067aa0ba902b7"
)

var (
	fixtureTraceID = trace.TraceID{
		0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6,
		0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36,
	}
	fixtureSpanID = trace.SpanID{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7}
)

// mustSink wraps w in a Sink or fails the test.
func mustSink(t *testing.T, w io.Writer) logger.Sink {
	t.Helper()
	sink, err := logger.NewWriterSink(w)
	if err != nil {
		t.Fatalf("NewWriterSink err = %v", err)
	}
	return sink
}

// tracedContext returns a context carrying the fixture span, i.e. what an
// inbound request looks like once trace.Extract has read the traceparent.
func tracedContext(ctx context.Context) context.Context {
	return trace.ContextWithSpanContext(ctx, trace.SpanContext{
		TraceID: fixtureTraceID,
		SpanID:  fixtureSpanID,
		Flags:   trace.FlagSampled,
	})
}

// TestTraceContextFromContext covers the adapter in isolation: a valid span
// crosses into the logging domain, and everything else — no span at all, a
// span context stored with an all-zero identifier — yields the zero value,
// which the encoders render as nothing.
func TestTraceContextFromContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		build func() context.Context
		want  bool
	}{
		{
			name:  "span in scope crosses over",
			build: func() context.Context { return tracedContext(context.Background()) },
			want:  true,
		},
		{
			name:  "no span in scope yields the zero value",
			build: context.Background,
			want:  false,
		},
		{
			name: "an all-zero span context is refused, not copied through",
			build: func() context.Context {
				//: ContextWithSpanContext deliberately stores an INVALID
				//: context to shadow an outer one; the logger must read that
				//: as "no trace here" rather than stamp 32 zeroes.
				return trace.ContextWithSpanContext(context.Background(), trace.SpanContext{})
			},
			want: false,
		},
		{
			name: "a trace id with no span id is refused",
			build: func() context.Context {
				return trace.ContextWithSpanContext(context.Background(), trace.SpanContext{TraceID: fixtureTraceID})
			},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := logger.TraceContextFromContext(tc.build())
			if got.IsValid() != tc.want {
				t.Fatalf("TraceContextFromContext().IsValid() = %v, want %v", got.IsValid(), tc.want)
			}
			if !tc.want {
				//: the zero value, not merely an invalid one — nothing must
				//: survive from a refused span context.
				if got != (logger.TraceContext{}) {
					t.Errorf("TraceContextFromContext() = %v, want the zero value", got)
				}
				return
			}
			if string(got.AppendTraceIDHex(nil)) != fixtureTraceHex {
				t.Errorf("trace id = %q, want %q", got.AppendTraceIDHex(nil), fixtureTraceHex)
			}
			if string(got.AppendSpanIDHex(nil)) != fixtureSpanHex {
				t.Errorf("span id = %q, want %q", got.AppendSpanIDHex(nil), fixtureSpanHex)
			}
		})
	}
}

// TestNewTextCorrelatesWithTheSpanInScope is the ticket end to end on the
// NewText path: no caller changed a line of code, and a record emitted inside
// a span now carries the two identifiers an operator pastes from a tracing
// backend — while a record emitted outside one carries neither key.
func TestNewTextCorrelatesWithTheSpanInScope(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg, err := logger.NewText(logger.Config{Writer: &buf, MinLevel: logger.LevelInfo})
	if err != nil {
		t.Fatalf("NewText err = %v", err)
	}
	logger.Info(tracedContext(t.Context()), lg, "inside a span")
	logger.Info(t.Context(), lg, "outside any span")
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "trace_id="+fixtureTraceHex) || !strings.Contains(lines[0], "span_id="+fixtureSpanHex) {
		t.Errorf("in-span line = %q, want both correlation fields", lines[0])
	}
	//: the whole point of the absent case: no key, no empty value, no zeroes.
	if strings.Contains(lines[1], "trace_id") || strings.Contains(lines[1], "span_id") {
		t.Errorf("out-of-span line = %q, want no correlation field at all", lines[1])
	}
	if strings.Contains(lines[1], strings.Repeat("0", 32)) {
		t.Errorf("out-of-span line = %q, want no all-zero identifier", lines[1])
	}
}

// TestEveryEmissionPathCarriesTheSpan pins that correlation is a property of
// the Logger and not of one entry point: the variadic helpers, the slice
// overload and the chainable builder all stamp the same identity, and so do
// the loggers derived by With and WithGroup.
func TestEveryEmissionPathCarriesTheSpan(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	base, err := logger.NewWithSink(logger.SinkConfig{
		Sink:     mustSink(t, &buf),
		Encoder:  logger.TextEncoder(),
		MinLevel: logger.LevelDebug,
	})
	if err != nil {
		t.Fatalf("NewWithSink err = %v", err)
	}
	ctx := tracedContext(t.Context())
	logger.Info(ctx, base, "variadic")
	logger.LogAttrs(ctx, base, logger.LevelInfo, "slice", []logger.Attr{logger.String("k", "v")})
	logger.Build(base, logger.LevelInfo).Str("k", "v").Send(ctx, "builder")
	logger.Info(ctx, base.With(logger.String("bound", "1")), "with")
	logger.Info(ctx, base.WithGroup("http"), "withgroup")
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 5: %q", len(lines), buf.String())
	}
	for _, line := range lines {
		if !strings.Contains(line, "trace_id="+fixtureTraceHex) {
			t.Errorf("line = %q, want the trace id on every emission path", line)
		}
	}
	//: WithGroup namespaces attributes, and MUST NOT namespace the two
	//: top-level correlation fields — "http.trace_id" is not a field name any
	//: ingestion pipeline recognises.
	if strings.Contains(lines[4], "http.trace_id") {
		t.Errorf("grouped line = %q, want an unprefixed trace_id", lines[4])
	}
}

// TestJSONEncoderTopLevelKeysEndToEnd asserts the shape on the parsed object
// rather than on a substring, because "top-level key" is a statement about the
// JSON document and not about the bytes.
func TestJSONEncoderTopLevelKeysEndToEnd(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg, err := logger.NewWithSink(logger.SinkConfig{
		Sink:    mustSink(t, &buf),
		Encoder: logger.NewJSONEncoder(),
	})
	if err != nil {
		t.Fatalf("NewWithSink err = %v", err)
	}
	logger.Info(tracedContext(t.Context()), lg.WithGroup("http"), "served")
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal(%q) err = %v", buf.String(), err)
	}
	if got["trace_id"] != fixtureTraceHex || got["span_id"] != fixtureSpanHex {
		t.Errorf("object = %v, want trace_id/span_id as top-level keys", got)
	}
}

// TestMemorySinkRecordCarriesTheTraceContext proves a Sink sees the identity on
// the record itself, not only in the formatted bytes — which is what lets a
// structured transport (syslog, CloudWatch, a DB writer) put it in its own
// field rather than parse it back out of a line.
func TestMemorySinkRecordCarriesTheTraceContext(t *testing.T) {
	t.Parallel()
	sink := logger.NewMemorySink()
	lg, err := logger.NewWithSink(logger.SinkConfig{Sink: sink, Encoder: logger.TextEncoder()})
	if err != nil {
		t.Fatalf("NewWithSink err = %v", err)
	}
	logger.Info(tracedContext(t.Context()), lg, "recorded")
	records := sink.Records()
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if !records[0].TraceContext.IsValid() {
		t.Fatal("record carries no trace context")
	}
	if string(records[0].TraceContext.AppendTraceIDHex(nil)) != fixtureTraceHex {
		t.Errorf("record trace id = %q, want %q", records[0].TraceContext.AppendTraceIDHex(nil), fixtureTraceHex)
	}
}
