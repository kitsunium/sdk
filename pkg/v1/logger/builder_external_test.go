package logger_test

import (
	"context"
	"os"
	"path/filepath"
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
			child := lg.WithGroup("http")
			logger.Build(child, logger.LevelInfo).Str("method", "GET").Send(t.Context(), "req")
			if !strings.Contains(string(sink.captured), "http.method=\"GET\"") {
				t.Errorf("missing namespaced key: %q", sink.captured)
			}
		})
	}
}

// Test_Build is the name-matched test for Build (KTN-TEST-SYNC/COVERAGE). It
// asserts Build returns a usable, chainable Builder bound to the supplied Logger
// — the hot-path entry point the other builder tests drive through.
func Test_Build(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		level logger.Level
		key   string
		val   string
		want  string
	}

	tests := []tc{
		{name: "build at info emits the chained attr", level: logger.LevelInfo, key: "user", val: "ada", want: "user=\"ada\""},
		{name: "build at error emits the chained attr", level: logger.LevelError, key: "code", val: "x99", want: "code=\"x99\""},
	}

	runCase := func(t *testing.T, level logger.Level, key, val, want string) {
		t.Helper()
		sink := &builderSink{}
		lg := mustNewWithSink(t, sink)
		b := logger.Build(lg, level)
		//: Build must return a non-nil, chainable Builder.
		if b == nil {
			t.Fatalf("Build returned nil")
		}
		b.Str(key, val).Send(t.Context(), "msg")
		//: the returned Builder must be wired to lg — its output reaches the sink.
		if !strings.Contains(string(sink.captured), want) {
			t.Errorf("output %q missing %q", sink.captured, want)
		}
	}

	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.level, c.key, c.val, c.want)
		})
	}
}

// TestV116BenchDocRetractsZeroAllocClaim guards the consumer-facing BENCH.md
// against re-introducing the false zero-alloc framing for the Build path.
// Regression for V116 — the doc previously asserted that switching to Build
// "drops to near-zero" the steady-state byte cost, a guarantee the code
// (1 alloc/op, proven by TestV116BuildSendAllocatesOnePerEmit) does not provide.
func TestV116BenchDocRetractsZeroAllocClaim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		forbidden string
		required  string
	}{
		{
			name:      "Build byte cost no longer claimed near-zero",
			forbidden: "drops to near-zero",
			required:  "1 alloc/op",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(benchDocPath(t))
			if err != nil {
				t.Fatalf("read BENCH.md: %v", err)
			}
			doc := string(raw)
			//: the retracted phrase implied the Build path eliminates the byte cost.
			if strings.Contains(doc, tc.forbidden) {
				t.Errorf("BENCH.md still contains forbidden claim %q; the handler clones attrs on every Send (1 alloc/op) — see V116", tc.forbidden)
			}
			//: the corrected prose must own the per-Send clone so the doc stays honest.
			if !strings.Contains(doc, tc.required) {
				t.Errorf("BENCH.md no longer states %q; the measured reality must be documented — see V116", tc.required)
			}
		})
	}
}

// benchDocPath resolves pkg/v1/logger/BENCH.md under both run modes. Raw
// `go test` runs with cwd = package dir, so the bare filename resolves; the
// Bazel sandbox strips that relationship, so it ships BENCH.md as a runfile
// reachable via TEST_SRCDIR + TEST_WORKSPACE.
func benchDocPath(tb testing.TB) (path string) {
	tb.Helper()
	//: prefer the cwd-relative file when present (raw go test iteration).
	if _, err := os.Stat("BENCH.md"); err == nil {
		return "BENCH.md"
	}
	//: fall back to the Bazel runfiles layout where the data file is staged.
	if srcdir := os.Getenv("TEST_SRCDIR"); srcdir != "" {
		if wks := os.Getenv("TEST_WORKSPACE"); wks != "" {
			return filepath.Join(srcdir, wks, "pkg", "v1", "logger", "BENCH.md")
		}
	}
	//: neither layout resolved — the guard cannot run without the doc.
	tb.Fatalf("BENCH.md not found via cwd or TEST_SRCDIR/TEST_WORKSPACE")
	return ""
}

// foreignLogger is a Logger implementation NOT produced by svclogger.New, used
// to drive the type-assertion fallbacks inside Build / LogAttrs.
type foreignLogger struct{}

func (foreignLogger) Enabled(context.Context, logger.Level) bool                { return true }
func (foreignLogger) Log(context.Context, logger.Level, string, ...logger.Attr) {}
func (foreignLogger) With(...logger.Attr) logger.Logger                         { return foreignLogger{} }

func (foreignLogger) WithGroup(string) logger.Logger { return foreignLogger{} }
