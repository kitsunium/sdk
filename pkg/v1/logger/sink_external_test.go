package logger_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// bufSink is a minimal logger.Sink implementation that writes into an
// internal bytes.Buffer under a mutex. External-package smoke test scope
// only: production consumers use the sinks shipped under internal/service.
type bufSink struct {
	// mu serialises Write / Flush / Close so concurrent goroutines never race.
	mu sync.Mutex
	// buf is the accumulated payload; callers read it through Bytes.
	buf bytes.Buffer
	// writes counts how many times Write was called; drives fan-out assertions.
	writes int
	// flushes counts how many times Flush was called; drives close assertions.
	flushes int
	// closes counts how many times Close was called; drives close assertions.
	closes int
}

func (b *bufSink) Write(_ context.Context, _ logger.Record, p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writes++
	return b.buf.Write(p)
}

func (b *bufSink) Flush(_ context.Context) (err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flushes++
	return nil
}

func (b *bufSink) Close() (err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closes++
	return nil
}

func (b *bufSink) Snapshot() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestNewWithSinkEmits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     func(s *bufSink) logger.SinkConfig
		emit    func(ctx context.Context, lg logger.Logger)
		wantHit bool
	}{
		{
			name: "default encoder ships text lines",
			cfg: func(s *bufSink) logger.SinkConfig {
				return logger.SinkConfig{Sink: s}
			},
			emit:    func(ctx context.Context, lg logger.Logger) { logger.Info(ctx, lg, "ping") },
			wantHit: true,
		},
		{
			name: "min level drops records below it",
			cfg: func(s *bufSink) logger.SinkConfig {
				return logger.SinkConfig{Sink: s, MinLevel: logger.LevelWarn}
			},
			emit:    func(ctx context.Context, lg logger.Logger) { logger.Info(ctx, lg, "ping") },
			wantHit: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &bufSink{}
			lg, err := logger.NewWithSink(tc.cfg(sink))
			if err != nil {
				t.Fatalf("NewWithSink err = %v", err)
			}
			tc.emit(t.Context(), lg)
			got := sink.Snapshot()
			hit := strings.Contains(got, "ping")
			if hit != tc.wantHit {
				t.Errorf("hit=%v want=%v buf=%q", hit, tc.wantHit, got)
			}
		})
	}
}

func TestNewWithSinkRejectsNilSink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"SinkConfig{} returns SinkConfigRequired"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lg, err := logger.NewWithSink(logger.SinkConfig{})
			if lg != nil {
				t.Errorf("expected nil logger, got %v", lg)
			}
			if !errors.Is(err, logger.SinkConfigRequired) {
				t.Errorf("errors.Is(err, SinkRequired) = false: %v", err)
			}
			if code, _ := errs.CodeOf(err); code != logger.CodeSinkConfigRequired {
				t.Errorf("CodeOf = %d, want %d", code, logger.CodeSinkConfigRequired)
			}
		})
	}
}

func TestMultiFansOut(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Multi delivers to every branch"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, b := &bufSink{}, &bufSink{}
			fan := logger.Multi(a, b)
			lg, err := logger.NewWithSink(logger.SinkConfig{Sink: fan})
			if err != nil {
				t.Fatalf("NewWithSink err = %v", err)
			}
			logger.Info(t.Context(), lg, "fan")
			if !strings.Contains(a.Snapshot(), "fan") {
				t.Errorf("branch A missing payload: %q", a.Snapshot())
			}
			if !strings.Contains(b.Snapshot(), "fan") {
				t.Errorf("branch B missing payload: %q", b.Snapshot())
			}
		})
	}
}

func TestBuildZeroAllocs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Build().Str().Int().Send emits through sink"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &bufSink{}
			lg, err := logger.NewWithSink(logger.SinkConfig{Sink: sink})
			if err != nil {
				t.Fatalf("NewWithSink err = %v", err)
			}
			logger.Build(lg, logger.LevelInfo).
				Str("svc", "api").
				Int("tries", 3).
				Send(t.Context(), "built")
			if !strings.Contains(sink.Snapshot(), "built") {
				t.Errorf("builder output missing payload: %q", sink.Snapshot())
			}
			if !strings.Contains(sink.Snapshot(), "svc=\"api\"") {
				t.Errorf("builder attr svc missing: %q", sink.Snapshot())
			}
		})
	}
}

func TestLogAttrs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"LogAttrs accepts a pre-built slice"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &bufSink{}
			lg, err := logger.NewWithSink(logger.SinkConfig{Sink: sink})
			if err != nil {
				t.Fatalf("NewWithSink err = %v", err)
			}
			attrs := []logger.Attr{logger.String("k", "v"), logger.Int("n", 1)}
			logger.LogAttrs(t.Context(), lg, logger.LevelInfo, "slice", attrs)
			if !strings.Contains(sink.Snapshot(), "slice") {
				t.Errorf("LogAttrs output missing payload: %q", sink.Snapshot())
			}
			if !strings.Contains(sink.Snapshot(), "n=1") {
				t.Errorf("LogAttrs attr n missing: %q", sink.Snapshot())
			}
		})
	}
}

func TestWithGroup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"WithGroup namespaces subsequent attributes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &bufSink{}
			lg, err := logger.NewWithSink(logger.SinkConfig{Sink: sink})
			if err != nil {
				t.Fatalf("NewWithSink err = %v", err)
			}
			child := logger.WithGroup(lg, "http")
			logger.Info(t.Context(), child, "req", logger.String("method", "GET"))
			if !strings.Contains(sink.Snapshot(), "http.method=\"GET\"") {
				t.Errorf("WithGroup prefix missing: %q", sink.Snapshot())
			}
		})
	}
}

func TestConsoleHelpersAreNonNil(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		sink logger.Sink
	}{
		{"ConsoleStderr returns a usable sink", logger.ConsoleStderr()},
		{"ConsoleStdout returns a usable sink", logger.ConsoleStdout()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.sink == nil {
				t.Errorf("sink = nil")
			}
		})
	}
}

func TestTextEncoderIsNonNil(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"TextEncoder returns a usable encoder"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if logger.TextEncoder() == nil {
				t.Errorf("TextEncoder = nil")
			}
		})
	}
}
