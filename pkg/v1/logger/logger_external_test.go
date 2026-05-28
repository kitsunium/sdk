package logger_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	errs "github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/logger"
)

func mustNewText(tb testing.TB, cfg logger.Config) logger.Logger {
	tb.Helper()
	lg, err := logger.NewText(cfg)
	if err != nil {
		tb.Fatalf("NewText failed: %v", err)
	}
	return lg
}

func TestNewText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     logger.Config
		emit    func(ctx context.Context, lg logger.Logger)
		wantHit bool
	}{
		{
			name:    "explicit writer emits at configured level",
			cfg:     logger.Config{},
			emit:    func(ctx context.Context, lg logger.Logger) { logger.Info(ctx, lg, "ping") },
			wantHit: true,
		},
		{
			name:    "below configured level drops the record",
			cfg:     logger.Config{MinLevel: logger.LevelWarn},
			emit:    func(ctx context.Context, lg logger.Logger) { logger.Info(ctx, lg, "ping") },
			wantHit: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			cfg := tc.cfg
			cfg.Writer = &buf
			lg := mustNewText(t, cfg)
			tc.emit(t.Context(), lg)
			hit := strings.Contains(buf.String(), "ping")
			if hit != tc.wantHit {
				t.Errorf("hit=%v want=%v buf=%q", hit, tc.wantHit, buf.String())
			}
		})
	}
}

func TestNewTextRejectsNilWriter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Config{} returns WriterRequired"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lg, err := logger.NewText(logger.Config{})
			if lg != nil {
				t.Errorf("expected nil logger, got %v", lg)
			}
			if !errors.Is(err, logger.WriterRequired) {
				t.Errorf("errors.Is(err, WriterRequired) = false: %v", err)
			}
			if code, _ := errs.CodeOf(err); code != logger.CodeWriterRequired {
				t.Errorf("CodeOf = %v, want %v", code, logger.CodeWriterRequired)
			}
		})
	}
}

func TestNewTextInjectsFrameworkVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"every emitted line carries framework_version attr"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			lg := mustNewText(t, logger.Config{Writer: &buf, MinLevel: logger.LevelDebug})
			logger.Info(t.Context(), lg, "m")
			line := buf.String()
			if !strings.Contains(line, `framework_version="`+logger.FrameworkVersion()+`"`) {
				t.Errorf("missing framework_version attr: %q", line)
			}
		})
	}
}

// TestNewTextWriters exercises the Writers fan-out branch of NewText: a
// non-empty Writers slice broadcasts each record to every writer, while a
// nil entry surfaces WriterRequired so a typo in the slice fails fast.
func TestNewTextWriters(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		writers   func() []*bytes.Buffer
		withNil   bool
		wantErr   bool
		wantEvery bool
	}{
		{
			name:      "fan-out broadcasts to every writer",
			writers:   func() []*bytes.Buffer { return []*bytes.Buffer{{}, {}} },
			wantEvery: true,
		},
		{
			name:    "a nil writer in the slice yields WriterRequired",
			writers: func() []*bytes.Buffer { return []*bytes.Buffer{{}} },
			withNil: true,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bufs := tc.writers()
			//: assemble the io.Writer slice; the nil case prepends an untyped nil.
			ws := make([]io.Writer, 0, len(bufs)+1)
			//: the nil-entry case must put the nil first so it is hit before any buffer.
			if tc.withNil {
				ws = append(ws, nil)
			}
			for _, b := range bufs {
				ws = append(ws, b)
			}
			lg, err := logger.NewText(logger.Config{Writers: ws, MinLevel: logger.LevelDebug})
			//: error case — a nil writer must abort construction with WriterRequired.
			if tc.wantErr {
				if !errors.Is(err, logger.WriterRequired) {
					t.Errorf("errors.Is(err, WriterRequired) = false: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewText(Writers) failed: %v", err)
			}
			logger.Info(t.Context(), lg, "fanned")
			//: every writer in the fan-out must observe the same record.
			if tc.wantEvery {
				for i, b := range bufs {
					if !strings.Contains(b.String(), "fanned") {
						t.Errorf("writer %d missing record: %q", i, b.String())
					}
				}
			}
		})
	}
}

func TestDefault(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"default returns non-nil Logger"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lg, err := logger.Default()
			if err != nil {
				t.Errorf("Default err = %v", err)
			}
			if lg == nil {
				t.Errorf("Default returned nil")
			}
		})
	}
}

func TestDebug(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Debug emits DEBUG level"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			lg := mustNewText(t, logger.Config{Writer: &buf, MinLevel: logger.LevelDebug})
			logger.Debug(t.Context(), lg, "d")
			if !strings.Contains(buf.String(), "DEBUG") {
				t.Errorf("missing DEBUG in %q", buf.String())
			}
		})
	}
}

func TestInfo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Info emits INFO level"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			lg := mustNewText(t, logger.Config{Writer: &buf})
			logger.Info(t.Context(), lg, "i")
			if !strings.Contains(buf.String(), "INFO") {
				t.Errorf("missing INFO in %q", buf.String())
			}
		})
	}
}

func TestWarn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Warn emits WARN level"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			lg := mustNewText(t, logger.Config{Writer: &buf})
			logger.Warn(t.Context(), lg, "w")
			if !strings.Contains(buf.String(), "WARN") {
				t.Errorf("missing WARN in %q", buf.String())
			}
		})
	}
}

func TestError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Error emits ERROR level"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			lg := mustNewText(t, logger.Config{Writer: &buf})
			logger.Error(t.Context(), lg, "e")
			if !strings.Contains(buf.String(), "ERROR") {
				t.Errorf("missing ERROR in %q", buf.String())
			}
		})
	}
}

func TestString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		key  string
		val  string
	}{
		{"simple", "k", "v"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attr := logger.String(tc.key, tc.val)
			if attr.Value.Kind() != corelogger.KindString || attr.Value.String() != tc.val {
				t.Errorf("Value = %v (kind=%s), want %q (KindString)",
					attr.Value, attr.Value.Kind(), tc.val)
			}
		})
	}
}

func TestInt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  int
	}{
		{"positive", 42},
		{"negative", -42},
		{"zero", 0},
		{"max int", math.MaxInt},
		{"min int", math.MinInt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attr := logger.Int("k", tc.val)
			if attr.Value.Kind() != corelogger.KindInt64 || attr.Value.Int64() != int64(tc.val) {
				t.Errorf("Value = %v (kind=%s), want %d (KindInt64)",
					attr.Value, attr.Value.Kind(), tc.val)
			}
		})
	}
}

func TestBool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  bool
	}{
		{"true", true},
		{"false", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attr := logger.Bool("k", tc.val)
			if attr.Value.Kind() != corelogger.KindBool || attr.Value.Bool() != tc.val {
				t.Errorf("Value = %v (kind=%s), want %t (KindBool)",
					attr.Value, attr.Value.Kind(), tc.val)
			}
		})
	}
}

func TestFloat64(t *testing.T) {
	t.Parallel()
	//: zero is a runtime variable so the division-by-zero rows compute real
	//: IEEE-754 ±Inf / NaN payloads rather than tripping the constant-division
	//: compile error.
	zero := 0.0
	tests := []struct {
		name  string
		val   float64
		isNaN bool
	}{
		{name: "positive", val: 3.14},
		{name: "negative", val: -2.5},
		{name: "zero", val: 0},
		{name: "positive infinity (1/0)", val: 1.0 / zero},
		{name: "negative infinity (-1/0)", val: -1.0 / zero},
		{name: "NaN (0/0)", val: zero / zero, isNaN: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attr := logger.Float64("k", tc.val)
			//: every float row must keep the KindFloat64 discriminant.
			if attr.Value.Kind() != corelogger.KindFloat64 {
				t.Fatalf("Kind = %s, want KindFloat64", attr.Value.Kind())
			}
			got := attr.Value.Float64()
			//: NaN never equals itself, so the NaN row asserts via IsNaN.
			if tc.isNaN {
				if !math.IsNaN(got) {
					t.Errorf("Float64() = %g, want NaN", got)
				}
				return
			}
			//: finite and ±Inf rows round-trip under plain equality.
			if got != tc.val {
				t.Errorf("Float64() = %g, want %g", got, tc.val)
			}
		})
	}
}

func TestInt64(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  int64
	}{
		{"positive", 9_000_000_000},
		{"negative", -1},
		{"zero", 0},
		{"max int64", math.MaxInt64},
		{"min int64", math.MinInt64},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attr := logger.Int64("k", tc.val)
			if attr.Value.Kind() != corelogger.KindInt64 || attr.Value.Int64() != tc.val {
				t.Errorf("Value = %v (kind=%s), want %d (KindInt64)",
					attr.Value, attr.Value.Kind(), tc.val)
			}
		})
	}
}

func TestUint64(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  uint64
	}{
		{"zero", 0},
		{"large", 18_000_000_000_000_000_000},
		//: high-bit-set value would read as negative if mistaken for int64 —
		//: pins the unsigned round-trip.
		{"max uint64", math.MaxUint64},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attr := logger.Uint64("k", tc.val)
			if attr.Value.Kind() != corelogger.KindUint64 || attr.Value.Uint64() != tc.val {
				t.Errorf("Value = %v (kind=%s), want %d (KindUint64)",
					attr.Value, attr.Value.Kind(), tc.val)
			}
		})
	}
}

func TestDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  time.Duration
	}{
		{"positive seconds", 5 * time.Second},
		{"negative (clock skew)", -3 * time.Hour},
		{"zero", 0},
		{"max duration", math.MaxInt64},
		{"min duration", math.MinInt64},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attr := logger.Duration("k", tc.val)
			if attr.Value.Kind() != corelogger.KindDuration || attr.Value.Duration() != tc.val {
				t.Errorf("Value = %v (kind=%s), want %s (KindDuration)",
					attr.Value, attr.Value.Kind(), tc.val)
			}
		})
	}
}

func TestTime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  time.Time
	}{
		{"fixed instant", time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)},
		{"zero time", time.Time{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attr := logger.Time("k", tc.val)
			if attr.Value.Kind() != corelogger.KindTime || !attr.Value.Time().Equal(tc.val) {
				t.Errorf("Value = %v (kind=%s), want %s (KindTime)",
					attr.Value, attr.Value.Kind(), tc.val)
			}
		})
	}
}

func TestAny(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  any
	}{
		{"struct payload", struct{ X int }{X: 1}},
		{"nil payload", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attr := logger.Any("k", tc.val)
			if attr.Value.Kind() != corelogger.KindAny {
				t.Errorf("Kind = %s, want KindAny", attr.Value.Kind())
			}
			if attr.Value.Any() != tc.val {
				t.Errorf("Any() = %v, want %v", attr.Value.Any(), tc.val)
			}
		})
	}
}

func TestFrameworkVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"dev fallback when Version is unset"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if logger.FrameworkVersion() == "" {
				t.Errorf("FrameworkVersion returned empty string")
			}
		})
	}
}
