package logger

import (
	"context"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/logger/encoder"
	"github.com/kitsunium/sdk/internal/service/logger/sink/console"
)

func Test_genericHandler_Enabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		min     level.Level
		query   level.Level
		nilCtx  bool
		ctxDone bool
		want    bool
	}{
		{"above min with live ctx is enabled", level.Info, level.Warn, false, false, true},
		{"at min with live ctx is enabled", level.Info, level.Info, false, false, true},
		{"below min with live ctx is disabled", level.Info, level.Debug, false, false, false},
		{"nil ctx is treated as live", level.Info, level.Info, true, false, true},
		{"cancelled ctx disables regardless of level", level.Debug, level.Error, false, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := mustNewGeneric(t, tc.min)
			var ctx context.Context
			if tc.nilCtx {
				ctx = nil
			} else {
				ctx = t.Context()
				if tc.ctxDone {
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					ctx = cancelled
				}
			}
			rec := corelogger.RecordEvent{Level: tc.query}
			if got := h.Enabled(ctx, rec); got != tc.want {
				t.Errorf("Enabled(%v) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

func Test_genericHandler_Handle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		nilCtx      bool
		ctxDone     bool
		boundAttrs  []corelogger.AttrValue
		recordAttrs []corelogger.AttrValue
		wantErr     bool
	}{
		{"happy path with no attrs returns nil", false, false, nil, nil, false},
		{"happy path with handler-bound attrs returns nil", false, false, []corelogger.AttrValue{{Key: "svc"}}, nil, false},
		{"happy path with record + bound attrs returns nil", false, false, []corelogger.AttrValue{{Key: "svc"}}, []corelogger.AttrValue{{Key: "k"}}, false},
		{"nil ctx is treated as live and writes", true, false, nil, nil, false},
		{"cancelled ctx returns wrapped error", false, true, nil, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := mustNewGeneric(t, level.Debug)
			h.attrs = tc.boundAttrs
			var ctx context.Context
			if tc.nilCtx {
				ctx = nil
			} else {
				ctx = t.Context()
				if tc.ctxDone {
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					ctx = cancelled
				}
			}
			err := h.Handle(ctx, corelogger.RecordEvent{Level: level.Info, Message: "m", Attrs: tc.recordAttrs})
			if (err != nil) != tc.wantErr {
				t.Errorf("Handle err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func Test_genericHandler_WithAttrs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"WithAttrs returns a new handler with combined attrs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parent := mustNewGeneric(t, level.Debug)
			child := parent.WithAttrs([]corelogger.AttrValue{{Key: "k", Value: corelogger.StringValue("v")}})
			if child == nil {
				t.Fatal("WithAttrs returned nil")
			}
			gc, ok := child.(*genericHandler)
			if !ok {
				t.Fatalf("WithAttrs returned %T, want *genericHandler", child)
			}
			if len(gc.attrs) != 1 {
				t.Errorf("child attrs len = %d, want 1", len(gc.attrs))
			}
		})
	}
}

func Test_genericHandler_WithGroup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		group     string
		wantDepth int
	}{
		{"empty group is a no-op", "", 0},
		{"named group adds one level", "http", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parent := mustNewGeneric(t, level.Debug)
			child := parent.WithGroup(tc.group)
			if child == nil {
				t.Fatal("WithGroup returned nil")
			}
			gc, ok := child.(*genericHandler)
			if !ok {
				t.Fatalf("WithGroup returned %T, want *genericHandler", child)
			}
			if len(gc.groups) != tc.wantDepth {
				t.Errorf("child groups depth = %d, want %d", len(gc.groups), tc.wantDepth)
			}
		})
	}
}

func Test_mergeAttrs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		parent  []corelogger.AttrValue
		child   []corelogger.AttrValue
		wantLen int
	}{
		{"empty parent + empty child", nil, nil, 0},
		{
			"empty parent + non-empty child clones", nil,
			[]corelogger.AttrValue{{Key: "k"}},
			1,
		},
		{"non-empty parent + empty child concats", []corelogger.AttrValue{{Key: "p"}}, nil, 1},
		{
			"both non-empty concats parent first",
			[]corelogger.AttrValue{{Key: "p"}},
			[]corelogger.AttrValue{{Key: "c"}},
			2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mergeAttrs(tc.parent, tc.child)
			if len(got) != tc.wantLen {
				t.Errorf("mergeAttrs len = %d, want %d", len(got), tc.wantLen)
			}
		})
	}
}

// Test_genericHandler_Handle_timeOwnership is the V25 regression: Time is
// owned by the handler layer. A zero RecordEvent.Time MUST be stamped once
// from the injected clock (so the sink observes that instant, not the zero
// value — fails pre-fix when genericHandler held no clock), and a non-zero
// Time MUST NOT be restamped (guards against the V24 reintroduction).
func Test_genericHandler_Handle_timeOwnership(t *testing.T) {
	t.Parallel()
	clockInstant := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	callerInstant := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		recorder   time.Time
		wantInSink time.Time
	}{
		{"zero Time is stamped from the injected clock", time.Time{}, clockInstant},
		{"non-zero Time is preserved (no restamp)", callerInstant, callerInstant},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTimeOwnershipCase(t, clockInstant, tc.recorder, tc.wantInSink)
		})
	}
}

// runTimeOwnershipCase drives one V25 Time-ownership scenario: it builds a
// handler bound to a frozen clock, hands it a record carrying recorder, and
// asserts the capturing sink observed wantInSink.
func runTimeOwnershipCase(tb testing.TB, clockInstant, recorder, wantInSink time.Time) {
	tb.Helper()
	sink := &timeCaptureSink{}
	h := &genericHandler{
		enc:  encoder.NewText(frozenClock{at: clockInstant}),
		sink: sink,
		min:  level.Debug,
		clk:  frozenClock{at: clockInstant},
	}
	if err := h.Handle(context.Background(), corelogger.RecordEvent{Time: recorder, Level: level.Info, Message: "m"}); err != nil {
		tb.Fatalf("Handle err = %v", err)
	}
	if !sink.seen.Equal(wantInSink) {
		tb.Errorf("sink observed Time = %v, want %v", sink.seen, wantInSink)
	}
}

// Test_NewHandler_defaultsClockToSystem is the default-clock half of V25:
// NewHandler must wire clock.System so a zero-Time record is stamped with a
// live, non-zero instant.
func Test_NewHandler_defaultsClockToSystem(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"NewHandler wires a live system clock"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := mustNewGeneric(t, level.Debug)
			//: NewHandler must default clk to clock.System, never leave it nil.
			if h.clk == nil {
				t.Fatal("NewHandler left clk nil; want clock.System default")
			}
			//: a live clock yields a non-zero instant on Now.
			if h.clk.Now().IsZero() {
				t.Error("default clock returned zero time; want live system instant")
			}
		})
	}
}

func mustNewGeneric(tb testing.TB, min level.Level) *genericHandler {
	tb.Helper()
	enc := encoder.NewText(clock.System)
	sink, err := console.New(discardWriter{})
	if err != nil {
		tb.Fatalf("console.New err = %v", err)
	}
	h, err := NewHandler(enc, sink, min)
	if err != nil {
		tb.Fatalf("NewHandler err = %v", err)
	}
	gc, ok := h.(*genericHandler)
	if !ok {
		tb.Fatalf("NewHandler returned %T, want *genericHandler", h)
	}
	return gc
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// frozenClock returns a fixed instant so V25 timestamp ownership can be
// asserted deterministically.
type frozenClock struct {
	// at is the constant instant Now reports.
	at time.Time
}

// Now returns the frozen instant.
func (f frozenClock) Now() time.Time { return f.at }

// Since returns the gap between the frozen instant and t.
func (f frozenClock) Since(t time.Time) time.Duration { return f.at.Sub(t) }

// timeCaptureSink records the RecordEvent.Time it last observed so a test can
// assert which layer stamped the instant (V25).
type timeCaptureSink struct {
	// seen is the Time of the most recent record handed to Write.
	seen time.Time
}

// Write records the observed Time and discards the bytes.
func (s *timeCaptureSink) Write(_ context.Context, r corelogger.RecordEvent, p []byte) (int, error) {
	//: capture the instant the sink observed so the test can compare it.
	s.seen = r.Time
	return len(p), nil
}

// Flush is a no-op for the capture sink.
func (s *timeCaptureSink) Flush(_ context.Context) error { return nil }

// Close is a no-op for the capture sink.
func (s *timeCaptureSink) Close() error { return nil }
