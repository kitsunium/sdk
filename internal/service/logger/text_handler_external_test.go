package logger_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svclogger "github.com/kitsunium/sdk/internal/service/logger"
	"github.com/kitsunium/sdk/internal/service/logger/encoder"
)

// mustNewText is a black-box helper that constructs a TextHandler for tests
// that treat the happy-path construction as a precondition.
func mustNewText(tb testing.TB, w io.Writer, min level.Level) *svclogger.TextHandler {
	tb.Helper()
	h, err := svclogger.NewTextHandler(w, min)
	if err != nil {
		tb.Fatalf("NewTextHandler failed: %v", err)
	}
	return h
}

func TestNewTextHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		writerNil  bool
		wantNilRes bool
	}{
		{"real writer returns handler", false, false},
		{"nil writer returns nil", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var w *bytes.Buffer
			if !tc.writerNil {
				w = &bytes.Buffer{}
			}
			var got *svclogger.TextHandler
			var err error
			if tc.writerNil {
				got, err = svclogger.NewTextHandler(nil, level.Info)
				if err == nil {
					t.Errorf("NewTextHandler(nil,...) err = nil, want non-nil")
				}
			} else {
				got, err = svclogger.NewTextHandler(w, level.Info)
				if err != nil {
					t.Errorf("NewTextHandler(w,...) err = %v", err)
				}
			}
			if tc.wantNilRes && got != nil {
				t.Errorf("NewTextHandler(nil,...) = %v, want nil", got)
			}
			if !tc.wantNilRes && got == nil {
				t.Errorf("NewTextHandler(w,...) = nil, want non-nil")
			}
		})
	}
}

func TestTextHandler_Enabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		min         level.Level
		recLevel    level.Level
		ctxDone     bool
		wantEnabled bool
	}{
		{"record above min is enabled", level.Info, level.Warn, false, true},
		{"record at min is enabled", level.Info, level.Info, false, true},
		{"record below min is disabled", level.Info, level.Debug, false, false},
		{"cancelled context disables regardless of level", level.Debug, level.Error, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := mustNewText(t, &bytes.Buffer{}, tc.min)
			ctx := t.Context()
			if tc.ctxDone {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			rec := corelogger.RecordEvent{Level: tc.recLevel}
			if got := h.Enabled(ctx, rec); got != tc.wantEnabled {
				t.Errorf("Enabled(level=%v, ctxDone=%v) = %v, want %v", tc.recLevel, tc.ctxDone, got, tc.wantEnabled)
			}
		})
	}
}

func TestTextHandler_Handle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		minLevel   level.Level
		record     corelogger.RecordEvent
		wantInLine []string
		wantErr    bool
	}{
		{
			name:       "emits level and message",
			minLevel:   level.Debug,
			record:     corelogger.RecordEvent{Level: level.Info, Message: "hello"},
			wantInLine: []string{"INFO", "hello"},
		},
		{
			name:     "serialises string attrs with quotes",
			minLevel: level.Debug,
			record: corelogger.RecordEvent{
				Level:   level.Warn,
				Message: "audit",
				Attrs:   []corelogger.AttrValue{{Key: "user", Value: corelogger.StringValue("alice bob")}},
			},
			wantInLine: []string{"WARN", "audit", `user="alice bob"`},
		},
		{
			name:     "numeric, bool and float attrs rendered unquoted",
			minLevel: level.Debug,
			record: corelogger.RecordEvent{
				Level:   level.Debug,
				Message: "metrics",
				Attrs: []corelogger.AttrValue{
					{Key: "count", Value: corelogger.IntValue(7)},
					{Key: "big", Value: corelogger.Int64Value(42)},
					{Key: "ok", Value: corelogger.BoolValue(true)},
					{Key: "ratio", Value: corelogger.Float64Value(0.5)},
				},
			},
			wantInLine: []string{"count=7", "big=42", "ok=true", "ratio=0.5"},
		},
		{
			name:     "unknown value type renders as ?",
			minLevel: level.Debug,
			record: corelogger.RecordEvent{
				Level:   level.Info,
				Message: "oops",
				Attrs:   []corelogger.AttrValue{{Key: "x", Value: corelogger.AnyValue(struct{}{})}},
			},
			wantInLine: []string{"x=?"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			h := mustNewText(t, &buf, tc.minLevel)
			if err := h.Handle(t.Context(), tc.record); (err != nil) != tc.wantErr {
				t.Fatalf("Handle err = %v, wantErr = %v", err, tc.wantErr)
			}
			got := buf.String()
			for _, needle := range tc.wantInLine {
				if !strings.Contains(got, needle) {
					t.Errorf("output missing %q\nfull output: %q", needle, got)
				}
			}
			if !strings.HasSuffix(got, "\n") {
				t.Errorf("output does not end with newline: %q", got)
			}
		})
	}
}

func TestTextHandler_HandleHonoursCancelledContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"cancelled context returns ctx error without writing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			h := mustNewText(t, &buf, level.Debug)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			err := h.Handle(ctx, corelogger.RecordEvent{Level: level.Info, Message: "drop"})
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Handle(cancelled) err = %v, want context.Canceled", err)
			}
			if buf.Len() != 0 {
				t.Errorf("Handle(cancelled) wrote %d bytes, want 0", buf.Len())
			}
		})
	}
}

func TestTextHandler_WithAttrs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"child does not aliase parent attrs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			parent := mustNewText(t, &buf, level.Debug)
			parentWith := parent.WithAttrs([]corelogger.AttrValue{{Key: "p", Value: corelogger.StringValue("P")}})
			childWith := parentWith.WithAttrs([]corelogger.AttrValue{{Key: "c", Value: corelogger.StringValue("C")}})
			if err := childWith.Handle(t.Context(), corelogger.RecordEvent{Level: level.Info, Message: "m"}); err != nil {
				t.Fatalf("Handle err = %v", err)
			}
			line := buf.String()
			if !strings.Contains(line, `p="P"`) || !strings.Contains(line, `c="C"`) {
				t.Errorf("child output missing parent or child attrs: %q", line)
			}
			buf.Reset()
			if err := parentWith.Handle(t.Context(), corelogger.RecordEvent{Level: level.Info, Message: "m"}); err != nil {
				t.Fatalf("Handle err = %v", err)
			}
			if strings.Contains(buf.String(), `c="C"`) {
				t.Errorf("parent unexpectedly carries child attrs: %q", buf.String())
			}
		})
	}
}

// TestTextHandler_HandleIsConcurrentSafe launches short-lived goroutines via
// wg.Go; each worker returns after completing its writes, and wg.Wait joins
// them all before the test asserts on the aggregate output.
func TestTextHandler_HandleIsConcurrentSafe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		goroutines int
		perRoutine int
	}{
		{"10×50 concurrent writes produce 500 lines", 10, 50},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			h := mustNewText(t, &buf, level.Debug)
			var wg sync.WaitGroup
			for range tc.goroutines {
				wg.Go(func() {
					for range tc.perRoutine {
						if err := h.Handle(t.Context(), corelogger.RecordEvent{Level: level.Info, Message: "tick"}); err != nil {
							t.Errorf("Handle err = %v", err)
						}
					}
				})
			}
			wg.Wait()
			lines := strings.Count(buf.String(), "\n")
			want := tc.goroutines * tc.perRoutine
			if lines != want {
				t.Errorf("line count = %d, want %d", lines, want)
			}
		})
	}
}

func TestTextHandler_WithGroup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		groups    []string
		wantInfix string
	}{
		{"no groups renders bare key", nil, ` k="v"`},
		{"empty group is a no-op", []string{""}, ` k="v"`},
		{"single group prefixes the key", []string{"http"}, ` http.k="v"`},
		{"nested groups chain with dots", []string{"req", "http"}, ` req.http.k="v"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			var current corelogger.Handler = mustNewText(t, &buf, level.Debug)
			//: walk the tc.groups list to exercise WithGroup chaining.
			for _, g := range tc.groups {
				current = current.WithGroup(g)
			}
			rec := corelogger.RecordEvent{
				Level: level.Info, Message: "m",
				Attrs: []corelogger.AttrValue{{Key: "k", Value: corelogger.StringValue("v")}},
			}
			if err := current.Handle(t.Context(), rec); err != nil {
				t.Fatalf("Handle err = %v", err)
			}
			if !strings.Contains(buf.String(), tc.wantInfix) {
				t.Errorf("WithGroup output missing %q: %q", tc.wantInfix, buf.String())
			}
		})
	}
}

// forgeryProbeSpan is a valid W3C span identity (the traceparent example
// values), so a record carrying it renders trace_id/span_id on its line.
var forgeryProbeSpan = corelogger.TraceContextValue{
	TraceID: [corelogger.TraceIDLen]byte{
		0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6,
		0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36,
	},
	SpanID: [corelogger.SpanIDLen]byte{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7},
}

// TestTextHandler_HandleCannotForgeALine is the CWE-117 regression for the
// legacy single-writer handler — the one pkg/v1/logger's Default() and
// NewText(Config{Writer}) build. The composed text encoder has scrubbed CR, LF
// and NUL out of the message, the attribute keys and the group names since
// V110; this handler appended all three verbatim, so a "\n" in any of them
// started a line the application never wrote. Since ADR 0062 it was worse than
// a spoofed line: the trace context is rendered AFTER the message, so the real
// trace_id/span_id landed on the forged last line and lent it the request's
// identity. Every case must come out as exactly one line.
//
// SEEN FAILING against the unfixed handler, every case, for example:
//
//	LF in the message: 2 line breaks, want exactly the one ending the record:
//	  "2026-09-11T15:11:12.222+02:00 INFO a\nb k=\"v\"\n"
//	inside a span the ids stay on the one line: 2 line breaks, …:
//	  "… INFO a\nb trace_id=4bf92f3577b34da6a3ce929d0e0e4736 span_id=00f067aa0ba902b7 k=\"v\"\n"
//
// — the second is the real span's ids on the forged line. Reverting only the
// message scrub fails the three message cases, only the group-name scrub the
// group case, only the ungrouped key scrub the key case. No case renders a key
// UNDER a group, so that write site is left to the sweep below.
func TestTextHandler_HandleCannotForgeALine(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		message string
		key     string
		group   string
		trace   corelogger.TraceContextValue
		want    string
	}
	tests := []tc{
		{name: "LF in the message", message: "a\nb", key: "k", want: `INFO a b k="v"`},
		{name: "LF in an attribute key", message: "m", key: "k\nx", want: `INFO m k x="v"`},
		{name: "LF in a group name", message: "m", key: "k", group: "g\ny", want: `INFO m g y.k="v"`},
		{name: "CR and NUL are scrubbed as well", message: "a\rb\x00c", key: "k", want: `INFO a b c k="v"`},
		{
			name: "inside a span the ids stay on the one line", message: "a\nb", key: "k", trace: forgeryProbeSpan,
			want: `INFO a b trace_id=4bf92f3577b34da6a3ce929d0e0e4736 span_id=00f067aa0ba902b7 k="v"`,
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var buf bytes.Buffer
		var h corelogger.Handler = mustNewText(t, &buf, level.Debug)
		if tc.group != "" {
			h = h.WithGroup(tc.group)
		}
		rec := corelogger.RecordEvent{
			Level: level.Info, Message: tc.message, TraceContext: tc.trace,
			Attrs: []corelogger.AttrValue{{Key: tc.key, Value: corelogger.StringValue("v")}},
		}
		if err := h.Handle(t.Context(), rec); err != nil {
			t.Fatalf("%s: Handle err = %v", tc.name, err)
		}
		out := buf.String()
		//: the record terminator is the one legitimate line break.
		if got := strings.Count(out, "\n"); got != 1 || !strings.HasSuffix(out, "\n") {
			t.Fatalf("%s: %d line breaks, want exactly the one ending the record: %q", tc.name, got, out)
		}
		if strings.ContainsAny(out, "\r\x00") {
			t.Errorf("%s: a framing byte survived into the line: %q", tc.name, out)
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("%s: line %q does not carry %q", tc.name, out, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestTextHandler_FramesEveryByteAsTheEncoderDoes pins the WIRING of the
// framing scrub, not the scrub. There is one scrub — encoder.AppendSanitized —
// shared by the composed encoder and this handler, so a test comparing two
// copies of it would compare a function with itself. What can still go wrong
// is what did go wrong: a position the handler writes WITHOUT calling it. So
// every byte value is swept through each caller-influenced position — the
// message, an attribute key, an attribute key under a group (a separate write
// site: the grouped path renders its own key), a group name — and the
// handler's line must be byte-identical to encoder.NewText's line for the same
// record, and must contain exactly one line break.
//
// MUTATION-CHECKED, one call site at a time. The unfixed handler failed at
// bytes 0x00, 0x0a and 0x0d in all four positions and framed the other 253
// values identically. Reverting any ONE of the four encoder.AppendSanitized
// calls in text_handler.go to a raw append fails the position it writes and no
// other:
//
//	message: byte 0x0a framed as "… INFO x\ny trace_id=… k=\"v\"\n",
//	  the encoder frames "… INFO x y trace_id=… k=\"v\"\n"
//	group name: byte 0x0a framed as "… span_id=00f067aa0ba902b7 x\ny.k=\"v\"\n", …
//	attribute key under a group: byte 0x0a framed as "… g.x\ny=\"v\"\n", …
//	attribute key: byte 0x0a framed as "… x\ny=\"v\"\n", …
//
// The grouped-key mutation is caught by this test ALONE, which is why that
// position is listed separately from the ungrouped key.
func TestTextHandler_FramesEveryByteAsTheEncoderDoes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		build func(probe string) (groups []string, rec corelogger.RecordEvent)
	}
	attr := func(key string) []corelogger.AttrValue {
		return []corelogger.AttrValue{{Key: key, Value: corelogger.StringValue("v")}}
	}
	tests := []tc{
		{"message", func(probe string) ([]string, corelogger.RecordEvent) {
			return nil, corelogger.RecordEvent{Message: probe, Attrs: attr("k")}
		}},
		{"attribute key", func(probe string) ([]string, corelogger.RecordEvent) {
			return nil, corelogger.RecordEvent{Message: "m", Attrs: attr(probe)}
		}},
		{"attribute key under a group", func(probe string) ([]string, corelogger.RecordEvent) {
			return []string{"g"}, corelogger.RecordEvent{Message: "m", Attrs: attr(probe)}
		}},
		{"group name", func(probe string) ([]string, corelogger.RecordEvent) {
			return []string{probe}, corelogger.RecordEvent{Message: "m", Attrs: attr("k")}
		}},
	}
	enc := encoder.NewText(clock.System)
	stamp := time.Date(2026, time.September, 11, 12, 0, 0, 0, time.UTC)
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		for value := range 256 {
			groups, rec := tc.build("x" + string([]byte{byte(value)}) + "y")
			rec.Level, rec.Time, rec.TraceContext = level.Info, stamp, forgeryProbeSpan
			want := enc.Append(nil, groups, rec)
			var buf bytes.Buffer
			var h corelogger.Handler = mustNewText(t, &buf, level.Debug)
			for _, g := range groups {
				h = h.WithGroup(g)
			}
			if err := h.Handle(t.Context(), rec); err != nil {
				t.Fatalf("%s: byte 0x%02x: Handle err = %v", tc.name, value, err)
			}
			if !bytes.Equal(buf.Bytes(), want) {
				t.Errorf("%s: byte 0x%02x framed as %q, the encoder frames %q", tc.name, value, buf.Bytes(), want)
			}
			if got := bytes.Count(buf.Bytes(), []byte{'\n'}); got != 1 {
				t.Errorf("%s: byte 0x%02x produced %d line breaks, want 1", tc.name, value, got)
			}
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestTextHandler_ATopLevelAttributeCannotSpellTheSDKsOwnFields is the
// reservation, on the legacy single-writer handler: it renders trace_id and
// span_id itself rather than through the encoder, so it has its own two write
// sites and needs its own case. A record emitted inside a span carrying a
// caller attribute of the same name would otherwise put the key twice on one
// line, and a parser keeping the last wins would read the caller's value as
// the span the line came from — the identity the ADR 0062 correlation exists
// to be trusted for.
//
// Under a group the key already carries the group's prefix and is left alone,
// which is also what pins that the reservation is asked at the TOP level only.
//
// Seen failing with the reservation removed:
//
//	top level: line "… INFO m trace_id=4bf92f3577b34da6a3ce929d0e0e4736
//	  span_id=00f067aa0ba902b7 trace_id=\"forged\"\n" spells trace_id 2 times
func TestTextHandler_ATopLevelAttributeCannotSpellTheSDKsOwnFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		groups  []string
		wantSub string
	}{
		{"top level: the attribute is renamed", nil, ` attr.trace_id="forged"`},
		{"grouped: the prefix already keeps them apart", []string{"http"}, ` http.trace_id="forged"`},
	}
	runCase := func(t *testing.T, name string, groups []string, wantSub string) {
		t.Helper()
		var buf bytes.Buffer
		var h corelogger.Handler = mustNewText(t, &buf, level.Debug)
		//: walk the group list so the grouped write site is the one reached.
		for _, g := range groups {
			h = h.WithGroup(g)
		}
		rec := corelogger.RecordEvent{
			Level: level.Info, Message: "m", TraceContext: forgeryProbeSpan,
			Attrs: []corelogger.AttrValue{{Key: corelogger.TraceIDKey, Value: corelogger.StringValue("forged")}},
		}
		if err := h.Handle(t.Context(), rec); err != nil {
			t.Fatalf("%s: Handle err = %v", name, err)
		}
		line := buf.String()
		if !strings.Contains(line, wantSub) {
			t.Errorf("%s: line %q does not contain %q", name, line, wantSub)
		}
		//: one trace_id key on the line, whatever the caller logged.
		if got := strings.Count(line, " "+corelogger.TraceIDKey+"="); got != 1 {
			t.Errorf("%s: line %q spells %s %d times, want exactly 1", name, line, corelogger.TraceIDKey, got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc.name, tc.groups, tc.wantSub)
		})
	}
}
