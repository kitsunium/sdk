package logger_test

import (
	"context"
	"sync"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svclogger "github.com/kitsunium/sdk/internal/service/observe/logger"
	"github.com/kitsunium/sdk/internal/service/observe/logger/encoder"
)

// lineSink keeps a copy of every line and of every record's attribute keys.
type lineSink struct {
	mu    sync.Mutex
	lines []string
	keys  [][]string
}

func (s *lineSink) Write(_ context.Context, r corelogger.RecordEvent, p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, string(p))
	keys := make([]string, 0, len(r.Attrs))
	for _, a := range r.Attrs {
		keys = append(keys, a.Key)
	}
	s.keys = append(s.keys, keys)
	return len(p), nil
}
func (*lineSink) Flush(context.Context) error { return nil }
func (*lineSink) Close() error                { return nil }

// plainEncoder hides the PrefixEncoder sibling, so a handler over it renders
// the bound attributes with the record's on every emit — the path before #1016.
type plainEncoder struct{ inner corelogger.Encoder }

func (p plainEncoder) Name() string { return p.inner.Name() }
func (p plainEncoder) Append(dst []byte, groups []string, r corelogger.RecordEvent) []byte {
	return p.inner.Append(dst, groups, r)
}

// matrixAttrs is one attribute of every Kind, the ADR 0070 reserved keys and
// strings the encoders escape or scrub.
func matrixAttrs(tag string) []corelogger.AttrValue {
	at := time.Date(2026, 11, 3, 10, 0, 0, 123_000_000, time.UTC)
	return []corelogger.AttrValue{
		{Key: tag + "s", Value: corelogger.StringValue("a \"q\"\n\x01 é")},
		{Key: tag + "i", Value: corelogger.Int64Value(-42)},
		{Key: tag + "u", Value: corelogger.Uint64Value(1 << 63)},
		{Key: tag + "f", Value: corelogger.Float64Value(1e6)},
		{Key: tag + "b", Value: corelogger.BoolValue(true)},
		{Key: tag + "d", Value: corelogger.DurationValue(1500 * time.Millisecond)},
		{Key: tag + "t", Value: corelogger.TimeValue(at)},
		{Key: tag + "a", Value: corelogger.AnyValue(struct{ X int }{7})},
		{Key: tag + "g", Value: corelogger.GroupValue(corelogger.AttrValue{Key: "in", Value: corelogger.IntValue(1)})},
		{Key: corelogger.TraceIDKey, Value: corelogger.StringValue("spoof-" + tag)},
		{Key: "attr.span_id", Value: corelogger.StringValue("ns-" + tag)},
		{Key: "k\r\ney " + tag, Value: corelogger.StringValue("v")},
	}
}

// derivations are the bound-attribute shapes: attrs alone, attrs then a group,
// a group then attrs, and nested groups around two bindings.
var derivations = []struct {
	name  string
	apply func(h corelogger.Handler) corelogger.Handler
}{
	{"none", func(h corelogger.Handler) corelogger.Handler { return h }},
	{"attrs", func(h corelogger.Handler) corelogger.Handler { return h.WithAttrs(matrixAttrs("b")) }},
	{"attrs-then-group", func(h corelogger.Handler) corelogger.Handler {
		return h.WithAttrs(matrixAttrs("b")).WithGroup("g1")
	}},
	{"group-then-attrs", func(h corelogger.Handler) corelogger.Handler {
		return h.WithGroup("g\n1").WithAttrs(matrixAttrs("b"))
	}},
	{"nested", func(h corelogger.Handler) corelogger.Handler {
		return h.WithAttrs(matrixAttrs("b")).WithGroup("g1").WithAttrs(matrixAttrs("c")).WithGroup("g2")
	}},
}

// TestBoundAttrsRenderOnceAndTheLineIsUnchanged (#1016): for both encoders and
// every derivation shape, a handler that splices its pre-rendered bound
// attributes writes byte for byte the line a handler rendering them per
// record writes, inside a span and outside one, and the sink still receives
// the merged attributes.
func TestBoundAttrsRenderOnceAndTheLineIsUnchanged(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)
	encoders := map[string]corelogger.Encoder{
		"text": encoder.NewText(clock.System),
		"json": encoder.NewJSON(clock.System),
	}
	for encName, enc := range encoders {
		if _, ok := enc.(corelogger.PrefixEncoder); !ok {
			t.Fatalf("the %s encoder is not a PrefixEncoder", encName)
		}
		for _, d := range derivations {
			spliced, plain := &lineSink{}, &lineSink{}
			hs, err := svclogger.NewHandler(enc, spliced, level.Debug)
			if err != nil {
				t.Fatal(err)
			}
			hp, err := svclogger.NewHandler(plainEncoder{enc}, plain, level.Debug)
			if err != nil {
				t.Fatal(err)
			}
			hs, hp = d.apply(hs), d.apply(hp)
			for _, tc := range []corelogger.TraceContextValue{{}, {TraceID: [16]byte{1, 2}, SpanID: [8]byte{3}}} {
				r := corelogger.RecordEvent{Time: at, Level: level.Warn, Message: "m\x1b[2K", Attrs: matrixAttrs("r"), TraceContext: tc}
				if err := hs.Handle(context.Background(), r); err != nil {
					t.Fatal(err)
				}
				if err := hp.Handle(context.Background(), r); err != nil {
					t.Fatal(err)
				}
			}
			for i := range plain.lines {
				if spliced.lines[i] != plain.lines[i] {
					t.Fatalf("%s/%s record %d:\nspliced %q\nplain   %q", encName, d.name, i, spliced.lines[i], plain.lines[i])
				}
				if len(spliced.keys[i]) != len(plain.keys[i]) {
					t.Fatalf("%s/%s record %d: the sink got %d attrs, want the merged %d", encName, d.name, i, len(spliced.keys[i]), len(plain.keys[i]))
				}
			}
		}
	}
}
