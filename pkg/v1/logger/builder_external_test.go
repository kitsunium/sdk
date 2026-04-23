package logger_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// builderSink captures the bytes the builder hands to the sink so each test
// case can assert on the encoded line shape.
type builderSink struct {
	// captured is the last payload accepted by Write.
	captured []byte
	// writes counts the number of Write calls.
	writes int
}

func (b *builderSink) Write(_ context.Context, _ logger.Record, p []byte) (int, error) {
	b.writes++
	b.captured = append(b.captured[:0], p...)
	return len(p), nil
}

func (b *builderSink) Flush(_ context.Context) error { return nil }
func (b *builderSink) Close() error                  { return nil }

func mustNewWithSink(tb testing.TB, sink logger.Sink) logger.Logger {
	tb.Helper()
	lg, err := logger.NewWithSink(logger.SinkConfig{Sink: sink})
	if err != nil {
		tb.Fatalf("NewWithSink err = %v", err)
	}
	return lg
}

func TestBuilderEmitsEachAttrType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		emit     func(b logger.Builder)
		contains string
	}{
		{"Str renders quoted string attr", func(b logger.Builder) { b.Str("k", "v") }, "k=\"v\""},
		{"Int renders int attr base-10", func(b logger.Builder) { b.Int("n", 7) }, "n=7"},
		{"Bool renders true literal", func(b logger.Builder) { b.Bool("ok", true) }, "ok=true"},
		{"Float64 renders shortest round-trip", func(b logger.Builder) { b.Float64("ratio", 0.5) }, "ratio=0.5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &builderSink{}
			lg := mustNewWithSink(t, sink)
			b := logger.Build(lg, logger.LevelInfo)
			tc.emit(b)
			b.Send(t.Context(), "msg")
			if !strings.Contains(string(sink.captured), tc.contains) {
				t.Errorf("payload missing %q: %q", tc.contains, sink.captured)
			}
		})
	}
}

func TestBuilderRejectsForeignLogger(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"foreign Logger yields nil Builder"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: build a Logger by a different package's New so the type assertion fails.
			b := logger.Build(foreignLogger{}, logger.LevelInfo)
			if b != nil {
				t.Errorf("Build returned non-nil Builder for foreign Logger")
			}
		})
	}
}

func TestLogAttrsIgnoresForeignLogger(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"foreign Logger silently no-ops without panic"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			panicked := false
			func() {
				defer func() {
					if r := recover(); r != nil {
						panicked = true
					}
				}()
				logger.LogAttrs(t.Context(), foreignLogger{}, logger.LevelInfo, "msg", []logger.Attr{logger.Int("n", 1)})
			}()
			if panicked {
				t.Errorf("LogAttrs panicked on foreign Logger, want silent drop")
			}
		})
	}
}

func TestWithGroupOnRealLogger(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"WithGroup namespaces subsequent Send attrs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &builderSink{}
			lg := mustNewWithSink(t, sink)
			child := logger.WithGroup(lg, "http")
			logger.Build(child, logger.LevelInfo).Str("method", "GET").Send(t.Context(), "req")
			if !strings.Contains(string(sink.captured), "http.method=\"GET\"") {
				t.Errorf("missing namespaced key: %q", sink.captured)
			}
		})
	}
}

// foreignLogger is a Logger implementation NOT produced by svclogger.New, used
// to drive the type-assertion fallbacks inside Build / LogAttrs.
type foreignLogger struct{}

func (foreignLogger) Enabled(context.Context, logger.Level) bool                { return true }
func (foreignLogger) Log(context.Context, logger.Level, string, ...logger.Attr) {}
func (foreignLogger) With(...logger.Attr) logger.Logger                         { return foreignLogger{} }
func (foreignLogger) WithGroup(string) logger.Logger                            { return foreignLogger{} }
