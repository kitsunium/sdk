package encoder_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/logger/encoder"
)

// The W3C Trace Context specification's own traceparent example, so the
// expected hex is read off the document rather than off this implementation.
const (
	traceHexFixture string = "4bf92f3577b34da6a3ce929d0e0e4736"
	spanHexFixture  string = "00f067aa0ba902b7"
)

var (
	traceIDFixture = [corelogger.TraceIDLen]byte{
		0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6,
		0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36,
	}
	spanIDFixture = [corelogger.SpanIDLen]byte{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7}
)

// encodedRecord renders r through enc at a fixed instant.
func encodedRecord(enc encoder.Encoder, groups []string, r corelogger.RecordEvent) string {
	r.Time = time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	return string(enc.Append(nil, groups, r))
}

// TestTextEncoderRendersTraceContext covers the two halves of the decision at
// once: a record emitted inside a span carries trace_id and span_id as
// top-level fields, and a record emitted outside one carries NEITHER — no
// empty value, no all-zero identifier, not even the key.
func TestTextEncoderRendersTraceContext(t *testing.T) {
	t.Parallel()
	enc := encoder.NewText(clock.System)
	tests := []struct {
		name       string
		tc         corelogger.TraceContextValue
		wantSubs   []string
		wantAbsent bool
	}{
		{
			name:     "valid span renders both ids in lowercase hex",
			tc:       corelogger.TraceContextValue{TraceID: traceIDFixture, SpanID: spanIDFixture},
			wantSubs: []string{" trace_id=" + traceHexFixture, " span_id=" + spanHexFixture},
		},
		{
			name:       "no span renders nothing at all",
			tc:         corelogger.TraceContextValue{},
			wantAbsent: true,
		},
		{
			name:       "trace id without span id renders nothing",
			tc:         corelogger.TraceContextValue{TraceID: traceIDFixture},
			wantAbsent: true,
		},
		{
			name:       "span id without trace id renders nothing",
			tc:         corelogger.TraceContextValue{SpanID: spanIDFixture},
			wantAbsent: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			line := encodedRecord(enc, nil, corelogger.RecordEvent{
				Level: level.Info, Message: "m", TraceContext: tc.tc,
			})
			if tc.wantAbsent {
				//: the KEY must be absent, not merely empty — an operator
				//: filtering on trace_id must not match every line the
				//: service logs outside a request.
				if strings.Contains(line, "trace_id") || strings.Contains(line, "span_id") {
					t.Errorf("line = %q, want no correlation field at all", line)
				}
				return
			}
			for _, want := range tc.wantSubs {
				if !strings.Contains(line, want) {
					t.Errorf("line = %q, want it to contain %q", line, want)
				}
			}
		})
	}
}

// TestTextEncoderTraceContextIgnoresGroups is the reason the identity is a
// RecordEvent FIELD and not two attributes: an attribute under WithGroup would
// render as "http.trace_id" and no ingestion pipeline would recognise it.
// OpenTelemetry prescribes trace_id / span_id as TOP-LEVEL keys.
func TestTextEncoderTraceContextIgnoresGroups(t *testing.T) {
	t.Parallel()
	line := encodedRecord(encoder.NewText(clock.System), []string{"http", "inbound"}, corelogger.RecordEvent{
		Level:        level.Info,
		Message:      "m",
		Attrs:        []corelogger.AttrValue{{Key: "method", Value: corelogger.StringValue("GET")}},
		TraceContext: corelogger.TraceContextValue{TraceID: traceIDFixture, SpanID: spanIDFixture},
	})
	if !strings.Contains(line, " trace_id="+traceHexFixture) {
		t.Errorf("line = %q, want an unprefixed trace_id field", line)
	}
	if strings.Contains(line, "http.trace_id") || strings.Contains(line, "inbound.span_id") {
		t.Errorf("line = %q, want the group stack NOT applied to the correlation fields", line)
	}
	//: the group stack still applies to genuine attributes.
	if !strings.Contains(line, "http.inbound.method=") {
		t.Errorf("line = %q, want the group prefix still applied to attrs", line)
	}
}

// TestJSONEncoderRendersTraceContext pins the JSON shape the OpenTelemetry
// compatibility specification shows: trace context as TOP-LEVEL keys of the
// log object, hex-encoded strings, absent entirely when there is no span.
func TestJSONEncoderRendersTraceContext(t *testing.T) {
	t.Parallel()
	enc := encoder.NewJSON(clock.System)
	tests := []struct {
		name    string
		tc      corelogger.TraceContextValue
		present bool
	}{
		{name: "valid span yields top-level keys", tc: corelogger.TraceContextValue{TraceID: traceIDFixture, SpanID: spanIDFixture}, present: true},
		{name: "no span yields no keys", tc: corelogger.TraceContextValue{}, present: false},
		{name: "half a pair yields no keys", tc: corelogger.TraceContextValue{TraceID: traceIDFixture}, present: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			line := encodedRecord(enc, []string{"http"}, corelogger.RecordEvent{
				Level:        level.Info,
				Message:      "m",
				Attrs:        []corelogger.AttrValue{{Key: "method", Value: corelogger.StringValue("GET")}},
				TraceContext: tc.tc,
			})
			//: decode with encoding/json so "top level" is asserted on the
			//: parsed object rather than on a substring of the bytes.
			var got map[string]any
			if err := json.Unmarshal([]byte(line), &got); err != nil {
				t.Fatalf("json.Unmarshal(%q) err = %v", line, err)
			}
			traceID, hasTrace := got["trace_id"]
			spanID, hasSpan := got["span_id"]
			if !tc.present {
				if hasTrace || hasSpan {
					t.Errorf("object = %v, want no correlation members", got)
				}
				return
			}
			if !hasTrace || !hasSpan {
				t.Fatalf("object = %v, want trace_id and span_id as top-level keys", got)
			}
			if traceID != traceHexFixture || spanID != spanHexFixture {
				t.Errorf("ids = %v / %v, want %q / %q", traceID, spanID, traceHexFixture, spanHexFixture)
			}
			//: an attribute under a group is still flattened to a dotted key,
			//: so the correlation members are demonstrably on a different path.
			if _, ok := got["http.method"]; !ok {
				t.Errorf("object = %v, want the grouped attr flattened to http.method", got)
			}
		})
	}
}
