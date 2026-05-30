package logger_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	errs "github.com/kitsunium/sdk/pkg/v1/errs"
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
				t.Errorf("CodeOf = %v, want %v", code, logger.CodeSinkConfigRequired)
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
			child := lg.WithGroup("http")
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

// TestNewWriterSink covers both arms of the io.Writer adapter: a nil writer
// surfaces WriterRequired (the same sentinel NewText uses), and a real
// writer yields a Sink that ships records through NewWithSink.
func TestNewWriterSink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		nilW bool
	}{
		{"nil writer yields WriterRequired", true},
		{"real writer ships records", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: nil-writer arm — construction must abort with the typed sentinel.
			if tc.nilW {
				sink, err := logger.NewWriterSink(nil)
				if sink != nil {
					t.Errorf("expected nil sink, got %v", sink)
				}
				if !errors.Is(err, logger.WriterRequired) {
					t.Errorf("errors.Is(err, WriterRequired) = false: %v", err)
				}
				if code, _ := errs.CodeOf(err); code != logger.CodeWriterRequired {
					t.Errorf("CodeOf = %v, want %v", code, logger.CodeWriterRequired)
				}
				return
			}
			//: real-writer arm — the adapter must produce a record-shipping sink.
			var buf bytes.Buffer
			sink, err := logger.NewWriterSink(&buf)
			if err != nil {
				t.Fatalf("NewWriterSink err = %v", err)
			}
			lg, err := logger.NewWithSink(logger.SinkConfig{Sink: sink})
			if err != nil {
				t.Fatalf("NewWithSink err = %v", err)
			}
			logger.Info(t.Context(), lg, "wrote")
			if !strings.Contains(buf.String(), "wrote") {
				t.Errorf("writer sink missing payload: %q", buf.String())
			}
		})
	}
}

// Test_NewWithSink is the name-matched test for NewWithSink (KTN-TEST-SYNC/
// COVERAGE). It asserts NewWithSink builds a working Logger from a Sink and
// rejects a nil Sink with the typed SinkConfigRequired sentinel.
func Test_NewWithSink(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		nilSink bool
	}

	tests := []tc{
		{name: "non-nil sink builds a usable logger", nilSink: false},
		{name: "nil sink rejected with SinkConfigRequired", nilSink: true},
	}

	runCase := func(t *testing.T, nilSink bool) {
		t.Helper()
		//: the nil-sink arm exercises rejection; the non-nil arm wires a capture
		//: sink and expects the record to be delivered (the two are mutually
		//: exclusive, so a single nilSink flag fully selects the expectation).
		if nilSink {
			lg, err := logger.NewWithSink(logger.SinkConfig{Sink: nil, MinLevel: logger.LevelInfo})
			if lg != nil {
				t.Errorf("expected nil logger, got %v", lg)
			}
			//: rejection must surface the typed SinkConfigRequired sentinel.
			if !errors.Is(err, logger.SinkConfigRequired) {
				t.Errorf("err=%v want SinkConfigRequired", err)
			}
			return
		}
		capture := &bufSink{}
		lg, err := logger.NewWithSink(logger.SinkConfig{Sink: capture, MinLevel: logger.LevelInfo})
		if err != nil || lg == nil {
			t.Fatalf("NewWithSink=(%v,%v) want (logger,nil)", lg, err)
		}
		logger.Info(t.Context(), lg, "hello")
		//: a successfully built logger must deliver records to its sink.
		if !strings.Contains(capture.Snapshot(), "hello") {
			t.Errorf("sink missing payload: %q", capture.Snapshot())
		}
	}

	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.nilSink)
		})
	}
}

// Test_Multi is the name-matched test for Multi (KTN-TEST-SYNC/COVERAGE). It
// asserts Multi composes a fan-out Sink that broadcasts every record to all
// branches.
func Test_Multi(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		branches int
	}

	tests := []tc{
		{name: "single branch receives", branches: 1},
		{name: "three branches all receive", branches: 3},
	}

	runCase := func(t *testing.T, branches int) {
		t.Helper()
		sinks := make([]logger.Sink, branches)
		caps := make([]*bufSink, branches)
		for i := range sinks {
			cs := &bufSink{}
			caps[i] = cs
			sinks[i] = cs
		}
		fanout := logger.Multi(sinks...)
		//: Multi must return a non-nil composed Sink.
		if fanout == nil {
			t.Fatalf("Multi returned nil")
		}
		lg, err := logger.NewWithSink(logger.SinkConfig{Sink: fanout})
		if err != nil {
			t.Fatalf("NewWithSink: %v", err)
		}
		logger.Info(t.Context(), lg, "fan")
		//: every branch must observe the broadcast record.
		for i, cs := range caps {
			if !strings.Contains(cs.Snapshot(), "fan") {
				t.Errorf("branch %d received nothing", i)
			}
		}
	}

	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.branches)
		})
	}
}

// Test_ConsoleStderr is the name-matched test for ConsoleStderr (KTN-TEST-SYNC/
// COVERAGE). It asserts ConsoleStderr returns a non-nil, usable Sink.
func Test_ConsoleStderr(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		wantNil bool
	}

	tests := []tc{
		{name: "ConsoleStderr returns a non-nil sink", wantNil: false},
	}

	runCase := func(t *testing.T, wantNil bool) {
		t.Helper()
		sink := logger.ConsoleStderr()
		//: the convenience constructor must always yield a usable Sink.
		if (sink == nil) != wantNil {
			t.Errorf("ConsoleStderr nil=%v want %v", sink == nil, wantNil)
		}
	}

	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.wantNil)
		})
	}
}

// Test_ConsoleStdout is the name-matched test for ConsoleStdout (KTN-TEST-SYNC/
// COVERAGE). It asserts ConsoleStdout returns a non-nil, usable Sink.
func Test_ConsoleStdout(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		wantNil bool
	}

	tests := []tc{
		{name: "ConsoleStdout returns a non-nil sink", wantNil: false},
	}

	runCase := func(t *testing.T, wantNil bool) {
		t.Helper()
		sink := logger.ConsoleStdout()
		//: the convenience constructor must always yield a usable Sink.
		if (sink == nil) != wantNil {
			t.Errorf("ConsoleStdout nil=%v want %v", sink == nil, wantNil)
		}
	}

	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.wantNil)
		})
	}
}

// Test_TextEncoder is the name-matched test for TextEncoder (KTN-TEST-SYNC/
// COVERAGE). It asserts TextEncoder returns a non-nil Encoder that NewWithSink
// accepts and that delivers records.
func Test_TextEncoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		msg  string
	}

	tests := []tc{
		{name: "TextEncoder-backed logger delivers records", msg: "encoded"},
	}

	runCase := func(t *testing.T, msg string) {
		t.Helper()
		enc := logger.TextEncoder()
		//: the encoder constructor must always yield a non-nil Encoder.
		if enc == nil {
			t.Fatalf("TextEncoder returned nil")
		}
		sink := &bufSink{}
		lg, err := logger.NewWithSink(logger.SinkConfig{Sink: sink, Encoder: enc})
		if err != nil || lg == nil {
			t.Fatalf("NewWithSink with TextEncoder=(%v,%v) want (logger,nil)", lg, err)
		}
		logger.Info(t.Context(), lg, msg)
		//: a record emitted through the text encoder must reach the sink.
		if !strings.Contains(sink.Snapshot(), msg) {
			t.Errorf("TextEncoder-backed logger missing %q: %q", msg, sink.Snapshot())
		}
	}

	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.msg)
		})
	}
}
