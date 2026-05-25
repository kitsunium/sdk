package logger_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svclogger "github.com/kitsunium/sdk/internal/service/logger"
	"github.com/kitsunium/sdk/internal/service/logger/encoder"
	"github.com/kitsunium/sdk/internal/service/logger/sink/console"
)

// mustBuilderLogger constructs a Logger backed by a captured bytes.Buffer
// for builder-flavoured tests. Reused so the call sites stay short.
func mustBuilderLogger(tb testing.TB, buf *bytes.Buffer, min level.Level) corelogger.Logger {
	tb.Helper()
	enc := encoder.NewText(clock.System)
	sink, err := console.New(buf)
	if err != nil {
		tb.Fatalf("console.New err = %v", err)
	}
	h, err := svclogger.NewHandler(enc, sink, min)
	if err != nil {
		tb.Fatalf("NewHandler err = %v", err)
	}
	lg, err := svclogger.New(h)
	if err != nil {
		tb.Fatalf("New err = %v", err)
	}
	return lg
}

func TestBuild_ChainedAttrsLandInOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		runner  func(*testing.T, corelogger.Logger)
		needles []string
	}{
		{
			name: "Str + Int + Bool + Float64 chain renders all attrs",
			runner: func(t *testing.T, lg corelogger.Logger) {
				svclogger.Build(lg, level.Info).
					Str("k", "v").
					Int("n", 7).
					Bool("ok", true).
					Float64("ratio", 0.5).
					Send(t.Context(), "msg")
			},
			needles: []string{`k="v"`, "n=7", "ok=true", "ratio=0.5", "msg"},
		},
		{
			name: "Int64 + Uint64 + Duration + Time + Any cover the remaining typed paths",
			runner: func(t *testing.T, lg corelogger.Logger) {
				svclogger.Build(lg, level.Warn).
					Int64("big", 1000).
					Uint64("u", 42).
					Duration("d", time.Second).
					Time("t", time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)).
					Any("z", struct{}{}).
					Send(t.Context(), "wide")
			},
			needles: []string{"big=1000", "u=42", `d="1s"`, "t=2026-04-20T12:00:00Z", "z=?", "wide"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			lg := mustBuilderLogger(t, &buf, level.Debug)
			tc.runner(t, lg)
			out := buf.String()
			for _, needle := range tc.needles {
				if !strings.Contains(out, needle) {
					t.Errorf("output missing %q: %q", needle, out)
				}
			}
		})
	}
}

func TestBuild_DisabledLevelEmitsNothing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"sub-min level skips Send entirely"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			lg := mustBuilderLogger(t, &buf, level.Warn)
			svclogger.Build(lg, level.Debug).Str("k", "v").Send(t.Context(), "dropped")
			if buf.Len() != 0 {
				t.Errorf("disabled level wrote %d bytes, want 0", buf.Len())
			}
		})
	}
}

func TestBuild_ForeignLoggerReturnsNil(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"non-loggerImpl input yields nil Builder"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a nil Logger is the simplest foreign value the contract handles.
			if got := svclogger.Build(nil, level.Info); got != nil {
				t.Errorf("Build(nil) = %v, want nil", got)
			}
		})
	}
}
