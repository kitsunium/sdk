package slogbridge_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/logger"
	"github.com/kitsunium/sdk/pkg/v1/logger/slogbridge"
)

// newRecorder wires a Logger onto a MemorySink so a test can assert on the
// structured records the bridge produced rather than on rendered bytes.
func newRecorder(t *testing.T, min logger.Level) (*slog.Logger, *logger.MemorySink) {
	t.Helper()
	sink := logger.NewMemorySink()
	lg, err := logger.NewWithSink(logger.SinkConfig{Sink: sink, MinLevel: min})
	if err != nil {
		t.Fatalf("NewWithSink: %v", err)
	}
	sl, err := slogbridge.New(lg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return sl, sink
}

// attrsOf flattens the single expected record into key -> Attr pairs so
// assertions read as data rather than as index arithmetic.
func attrsOf(t *testing.T, recs []logger.RecordSnapshot) map[string]logger.Attr {
	t.Helper()
	if len(recs) != 1 {
		t.Fatalf("record count = %d, want 1", len(recs))
	}
	out := make(map[string]logger.Attr, len(recs[0].Attrs))
	for _, a := range recs[0].Attrs {
		out[a.Key] = a
	}
	return out
}

// A nil Logger must be refused, not silently swallowed: the bridge's entire
// value is the guarantee that one pipeline carries everything, and a handler
// that discards would break that guarantee exactly where a caller trusted it.
func TestNilLoggerIsRefused(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		call func() error
	}
	tests := []tc{
		{"New", func() error { _, err := slogbridge.New(nil); return err }},
		{"NewHandler", func() error { _, err := slogbridge.NewHandler(nil); return err }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := c.call()
		if err == nil {
			t.Fatal("nil logger accepted")
		}
		if !errs.HasCode(err, slogbridge.CodeLoggerRequired) {
			t.Errorf("code = %v, want CodeLoggerRequired (1.1.1.1)", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The four named levels share their integers across the two scales, so the
// conversion must be exact — not merely order-preserving.
func TestLevelsMapExactly(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   slog.Level
		want logger.Level
	}
	tests := []tc{
		{"debug", slog.LevelDebug, logger.LevelDebug},
		{"info", slog.LevelInfo, logger.LevelInfo},
		{"warn", slog.LevelWarn, logger.LevelWarn},
		{"error", slog.LevelError, logger.LevelError},
		{"custom between named levels survives", slog.Level(2), logger.Level(2)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sl, sink := newRecorder(t, logger.LevelDebug)
		sl.Log(context.Background(), c.in, "m")
		recs := sink.Records()
		if len(recs) != 1 {
			t.Fatalf("record count = %d, want 1", len(recs))
		}
		if recs[0].Level != c.want {
			t.Errorf("level = %v (%d), want %v (%d)",
				recs[0].Level, recs[0].Level, c.want, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Out-of-range levels saturate instead of wrapping. Wrapping would silently
// turn an absurdly severe level into a debug one — the dangerous direction.
func TestOutOfRangeLevelsSaturate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   slog.Level
		want logger.Level
	}
	tests := []tc{
		{"far above error saturates high", slog.Level(1000), logger.Level(127)},
		{"far below debug saturates low", slog.Level(-1000), logger.Level(-128)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sl, sink := newRecorder(t, logger.Level(-128))
		sl.Log(context.Background(), c.in, "m")
		recs := sink.Records()
		if len(recs) != 1 {
			t.Fatalf("record count = %d, want 1", len(recs))
		}
		if recs[0].Level != c.want {
			t.Errorf("level = %d, want %d", recs[0].Level, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The SDK Logger's threshold is the ONLY threshold: that is what makes the
// "one pipeline" claim true. A record below it must never reach the sink,
// even though slog itself was given no level of its own.
func TestSDKThresholdGovernsTheSlogView(t *testing.T) {
	t.Parallel()
	sl, sink := newRecorder(t, logger.LevelWarn)

	sl.Info("dropped by the SDK threshold")
	sl.Debug("dropped by the SDK threshold")
	if got := len(sink.Records()); got != 0 {
		t.Fatalf("records below threshold = %d, want 0", got)
	}
	if sl.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Enabled(info) = true under a warn threshold")
	}

	sl.Warn("kept")
	if got := len(sink.Records()); got != 1 {
		t.Fatalf("records at threshold = %d, want 1", got)
	}
}

// Every slog Kind must keep its type across the bridge. Collapsing them into
// Any would compile and log, but would cost the encoders their type-aware
// rendering — durations would print as integers, times as opaque payloads.
func TestAttrKindsSurviveConversion(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		attr slog.Attr
		want logger.Kind
	}
	tests := []tc{
		{"string", slog.String("k", "v"), logger.KindString},
		{"int", slog.Int("k", 3), logger.KindInt64},
		{"int64", slog.Int64("k", 3), logger.KindInt64},
		{"uint64", slog.Uint64("k", 3), logger.KindUint64},
		{"float64", slog.Float64("k", 1.5), logger.KindFloat64},
		{"bool", slog.Bool("k", true), logger.KindBool},
		{"duration", slog.Duration("k", time.Second), logger.KindDuration},
		{"time", slog.Time("k", time.Unix(0, 0)), logger.KindTime},
		{"any falls back to Any", slog.Any("k", []int{1}), logger.KindAny},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sl, sink := newRecorder(t, logger.LevelDebug)
		sl.LogAttrs(context.Background(), slog.LevelInfo, "m", c.attr)
		got, ok := attrsOf(t, sink.Records())["k"]
		if !ok {
			t.Fatal(`attr "k" missing`)
		}
		if got.Value.Kind() != c.want {
			t.Errorf("kind = %v, want %v", got.Value.Kind(), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Groups flatten to the dotted keys the SDK encoders already emit, so a
// bridged record is indistinguishable from a native one.
func TestGroupsFlattenToDottedKeys(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		attr    slog.Attr
		wantKey string
	}
	tests := []tc{
		{"one level", slog.Group("g", slog.String("k", "v")), "g.k"},
		{
			"nested levels",
			slog.Group("g1", slog.Group("g2", slog.String("k", "v"))),
			"g1.g2.k",
		},
		{
			"empty group name inlines its members",
			slog.Group("", slog.String("k", "v")),
			"k",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sl, sink := newRecorder(t, logger.LevelDebug)
		sl.LogAttrs(context.Background(), slog.LevelInfo, "m", c.attr)
		if _, ok := attrsOf(t, sink.Records())[c.wantKey]; !ok {
			t.Errorf("key %q missing; got %v", c.wantKey, sink.Records()[0].Attrs)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// slog's elision rules are the handler's job, and the SDK handler has no
// notion of them — so the bridge must apply them itself.
func TestSlogElisionRulesAreHonoured(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		attr slog.Attr
	}
	tests := []tc{
		{"zero attr is dropped", slog.Attr{}},
		{"empty group is dropped", slog.Group("g")},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sl, sink := newRecorder(t, logger.LevelDebug)
		sl.LogAttrs(context.Background(), slog.LevelInfo, "m", c.attr)
		// framework_version is stamped by the SDK Logger on every record, so
		// it is the expected floor here — anything beyond it means the elided
		// attribute leaked through.
		for k := range attrsOf(t, sink.Records()) {
			if k != "framework_version" {
				t.Errorf("elided attr leaked as %q; got %v", k, sink.Records()[0].Attrs)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// A group is POSITIONAL in slog: it governs what is bound after it, never
// what was already bound. The SDK handler's own WithGroup applies its final
// group stack to every bound attr, so delegating to it would retroactively
// move "before" under "g". This test pins the difference the bridge exists
// to absorb — it is the reason WithGroup tracks a prefix instead of calling
// through to Logger.WithGroup.
func TestGroupsAreEndPositionalNotRetroactive(t *testing.T) {
	t.Parallel()
	sl, sink := newRecorder(t, logger.LevelDebug)

	sl.With(slog.String("before", "1")).
		WithGroup("g").
		With(slog.String("after", "2")).
		Info("m")

	got := attrsOf(t, sink.Records())
	if _, ok := got["before"]; !ok {
		t.Errorf(`"before" was requalified; got %v`, sink.Records()[0].Attrs)
	}
	if _, ok := got["g.after"]; !ok {
		t.Errorf(`"g.after" missing; got %v`, sink.Records()[0].Attrs)
	}
}

// A LogValuer must be resolved before conversion; an unresolved one would
// land in Any and print as an opaque payload instead of the value it stands
// for — the exact failure the stdlib handlers resolve away.
func TestLogValuerIsResolved(t *testing.T) {
	t.Parallel()
	sl, sink := newRecorder(t, logger.LevelDebug)

	sl.LogAttrs(context.Background(), slog.LevelInfo, "m", slog.Any("k", valuer{}))

	got := attrsOf(t, sink.Records())["k"]
	if got.Value.Kind() != logger.KindString {
		t.Fatalf("kind = %v, want KindString (LogValuer unresolved)", got.Value.Kind())
	}
	if got.Value.String() != "resolved" {
		t.Errorf("value = %q, want %q", got.Value.String(), "resolved")
	}
}

// valuer stands in for any consumer type implementing slog.LogValuer.
type valuer struct{}

// LogValue satisfies slog.LogValuer.
func (valuer) LogValue() slog.Value { return slog.StringValue("resolved") }

// The end-to-end claim of this package: a record emitted through the slog
// view comes out of the SDK pipeline carrying the SDK's own decorations. If
// "framework_version" is present, the record went through NewText's
// decorated Logger and not through a parallel slog handler.
func TestBridgedRecordsCarrySDKDecorations(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer

	lg, err := logger.NewText(logger.Config{Writer: &buf, MinLevel: logger.LevelInfo})
	if err != nil {
		t.Fatalf("NewText: %v", err)
	}
	sl, err := slogbridge.New(lg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sl.Info("bridged", slog.Duration("took", 1500*time.Millisecond))

	line := buf.String()
	if !strings.Contains(line, "framework_version=") {
		t.Errorf("bridged record lacks framework_version: %q", line)
	}
	if !strings.Contains(line, "INFO bridged") {
		t.Errorf("record not rendered by the SDK text handler: %q", line)
	}
	// A duration must render as a duration. Before the handler's Kind table
	// was completed it printed "?", which would have made the bridge lossy
	// for the single attribute type slog callers use most.
	if !strings.Contains(line, `took="1.5s"`) {
		t.Errorf("duration not rendered: %q", line)
	}
}
