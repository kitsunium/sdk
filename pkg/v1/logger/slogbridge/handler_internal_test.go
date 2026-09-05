package slogbridge

import (
	"log/slog"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// newTestHandler builds a handler over a MemorySink so a case can inspect what
// was emitted without parsing rendered bytes.
func newTestHandler(t *testing.T, min logger.Level) (handler, *logger.MemorySink) {
	t.Helper()
	sink := logger.NewMemorySink()
	lg, err := logger.NewWithSink(logger.SinkConfig{Sink: sink, MinLevel: min})
	if err != nil {
		t.Fatalf("NewWithSink: %v", err)
	}
	return handler{lg: lg}, sink
}

// Enabled must consult the SDK Logger and nothing else — that is what makes
// "one pipeline, one threshold" true rather than aspirational.
func Test_handler_Enabled(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		min   logger.Level
		level slog.Level
		want  bool
	}
	tests := []tc{
		{"below the threshold", logger.LevelWarn, slog.LevelInfo, false},
		{"at the threshold", logger.LevelWarn, slog.LevelWarn, true},
		{"above the threshold", logger.LevelWarn, slog.LevelError, true},
		{"everything passes at debug", logger.LevelDebug, slog.LevelDebug, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		hdl, _ := newTestHandler(t, c.min)
		if got := hdl.Enabled(t.Context(), c.level); got != c.want {
			t.Errorf("Enabled(%v) under %v = %v, want %v", c.level, c.min, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Handle reports no error by contract — the SDK emission path has none to
// forward — and what it emits must carry the record's level and message.
func Test_handler_Handle(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		level     slog.Level
		attrs     []slog.Attr
		wantKept  bool
		wantAttrs int
	}
	tests := []tc{
		{"a bare record", slog.LevelInfo, nil, true, 1},
		{"a record with an attr", slog.LevelInfo, []slog.Attr{slog.String("k", "v")}, true, 2},
		{"a record below the threshold", slog.LevelDebug, nil, false, 0},
		{
			"a group is flattened before emission", slog.LevelInfo,
			[]slog.Attr{slog.Group("g", slog.Int("n", 1))}, true, 2,
		},
		{"an elided attr never reaches the sink", slog.LevelInfo, []slog.Attr{{}}, true, 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		hdl, sink := newTestHandler(t, logger.LevelInfo)

		// A zero time exercises the documented path where the SDK handler
		// stamps the record from its own clock.
		rec := slog.NewRecord(time.Time{}, c.level, "m", 0)
		rec.AddAttrs(c.attrs...)
		if err := hdl.Handle(t.Context(), rec); err != nil {
			t.Fatalf("Handle: %v", err)
		}

		recs := sink.Records()
		if c.wantKept != (len(recs) == 1) {
			t.Fatalf("records = %d, want kept=%v", len(recs), c.wantKept)
		}
		if !c.wantKept {
			return
		}
		if recs[0].Message != "m" {
			t.Errorf("message = %q, want %q", recs[0].Message, "m")
		}
		// framework_version is stamped by the SDK Logger, so it is the floor.
		if len(recs[0].Attrs) != c.wantAttrs {
			t.Errorf("attrs = %d, want %d (%v)", len(recs[0].Attrs), c.wantAttrs, recs[0].Attrs)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// WithAttrs derives rather than mutates, and binds under the chain active at
// the moment of the call — that is what makes slog's positional groups work.
func Test_handler_WithAttrs(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		prefix    string
		attrs     []slog.Attr
		wantDeriv bool
	}
	tests := []tc{
		{"binding an attr derives", "", []slog.Attr{slog.String("k", "v")}, true},
		{"binding under a chain derives", "g", []slog.Attr{slog.String("k", "v")}, true},
		{"an empty slice is a no-op", "", nil, false},
		{"a slice of elided attrs is a no-op", "", []slog.Attr{{}}, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		parent, _ := newTestHandler(t, logger.LevelDebug)
		parent.prefix = c.prefix

		child, ok := parent.WithAttrs(c.attrs).(handler)
		if !ok {
			t.Fatal("derived handler is not the concrete type")
		}
		if got := child.lg != parent.lg; got != c.wantDeriv {
			t.Errorf("derived = %v, want %v", got, c.wantDeriv)
		}
		// The chain must survive the derivation either way.
		if child.prefix != c.prefix {
			t.Errorf("prefix = %q, want %q", child.prefix, c.prefix)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// WithGroup extends the prefix and shares the Logger untouched, so attrs
// already bound keep the qualification they were given.
func Test_handler_WithGroup(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		prefix string
		group  string
		want   string
	}
	tests := []tc{
		{"opens the first level", "", "g", "g"},
		{"extends an existing chain", "g1", "g2", "g1.g2"},
		{"an empty name is a no-op", "g1", "", "g1"},
		{"an empty name with no chain is a no-op", "", "", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		parent, _ := newTestHandler(t, logger.LevelDebug)
		parent.prefix = c.prefix

		child, ok := parent.WithGroup(c.group).(handler)
		if !ok {
			t.Fatal("derived handler is not the concrete type")
		}
		if child.prefix != c.want {
			t.Errorf("prefix = %q, want %q", child.prefix, c.want)
		}
		// The Logger is shared: only the chain moves.
		if child.lg != parent.lg {
			t.Error("WithGroup derived a new Logger; it must only extend the chain")
		}
		if parent.prefix != c.prefix {
			t.Errorf("parent prefix mutated to %q", parent.prefix)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
