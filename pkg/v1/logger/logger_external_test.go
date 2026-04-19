package logger_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
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
				t.Errorf("CodeOf = %d, want %d", code, logger.CodeWriterRequired)
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
			if attr.Value != tc.val {
				t.Errorf("Value = %v, want %q", attr.Value, tc.val)
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
		{"zero", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attr := logger.Int("k", tc.val)
			if attr.Value != tc.val {
				t.Errorf("Value = %v, want %d", attr.Value, tc.val)
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
