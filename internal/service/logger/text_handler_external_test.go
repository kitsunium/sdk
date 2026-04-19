package logger_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/level"
	svclogger "github.com/kitsunium/sdk/internal/service/logger"
)

// mustNewText is a black-box helper that constructs a TextHandler for tests
// that treat the happy-path construction as a precondition.
func mustNewText(tb testing.TB, w io.Writer, min level.Level) *svclogger.TextHandler {
	tb.Helper()
	h, err := svclogger.NewTextHandler(w, min)
	if err != nil {
		tb.Fatalf("NewTextHandler failed: %v", err)
	}
	return h
}

func TestNewTextHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		writerNil  bool
		wantNilRes bool
	}{
		{"real writer returns handler", false, false},
		{"nil writer returns nil", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var w *bytes.Buffer
			if !tc.writerNil {
				w = &bytes.Buffer{}
			}
			var got *svclogger.TextHandler
			var err error
			if tc.writerNil {
				got, err = svclogger.NewTextHandler(nil, level.Info)
				if err == nil {
					t.Errorf("NewTextHandler(nil,...) err = nil, want non-nil")
				}
			} else {
				got, err = svclogger.NewTextHandler(w, level.Info)
				if err != nil {
					t.Errorf("NewTextHandler(w,...) err = %v", err)
				}
			}
			if tc.wantNilRes && got != nil {
				t.Errorf("NewTextHandler(nil,...) = %v, want nil", got)
			}
			if !tc.wantNilRes && got == nil {
				t.Errorf("NewTextHandler(w,...) = nil, want non-nil")
			}
		})
	}
}

func TestTextHandler_Enabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		min         level.Level
		recLevel    level.Level
		ctxDone     bool
		wantEnabled bool
	}{
		{"record above min is enabled", level.Info, level.Warn, false, true},
		{"record at min is enabled", level.Info, level.Info, false, true},
		{"record below min is disabled", level.Info, level.Debug, false, false},
		{"cancelled context disables regardless of level", level.Debug, level.Error, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := mustNewText(t, &bytes.Buffer{}, tc.min)
			ctx := t.Context()
			if tc.ctxDone {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			rec := corelogger.RecordEvent{Level: tc.recLevel}
			if got := h.Enabled(ctx, rec); got != tc.wantEnabled {
				t.Errorf("Enabled(level=%v, ctxDone=%v) = %v, want %v", tc.recLevel, tc.ctxDone, got, tc.wantEnabled)
			}
		})
	}
}

func TestTextHandler_Handle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		minLevel   level.Level
		record     corelogger.RecordEvent
		wantInLine []string
		wantErr    bool
	}{
		{
			name:       "emits level and message",
			minLevel:   level.Debug,
			record:     corelogger.RecordEvent{Level: level.Info, Message: "hello"},
			wantInLine: []string{"INFO", "hello"},
		},
		{
			name:     "serialises string attrs with quotes",
			minLevel: level.Debug,
			record: corelogger.RecordEvent{
				Level:   level.Warn,
				Message: "audit",
				Attrs:   []corelogger.AttrValue{{Key: "user", Value: "alice bob"}},
			},
			wantInLine: []string{"WARN", "audit", `user="alice bob"`},
		},
		{
			name:     "numeric, bool and float attrs rendered unquoted",
			minLevel: level.Debug,
			record: corelogger.RecordEvent{
				Level:   level.Debug,
				Message: "metrics",
				Attrs: []corelogger.AttrValue{
					{Key: "count", Value: 7},
					{Key: "big", Value: int64(42)},
					{Key: "ok", Value: true},
					{Key: "ratio", Value: 0.5},
				},
			},
			wantInLine: []string{"count=7", "big=42", "ok=true", "ratio=0.5"},
		},
		{
			name:     "unknown value type renders as ?",
			minLevel: level.Debug,
			record: corelogger.RecordEvent{
				Level:   level.Info,
				Message: "oops",
				Attrs:   []corelogger.AttrValue{{Key: "x", Value: struct{}{}}},
			},
			wantInLine: []string{"x=?"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			h := mustNewText(t, &buf, tc.minLevel)
			if err := h.Handle(t.Context(), tc.record); (err != nil) != tc.wantErr {
				t.Fatalf("Handle err = %v, wantErr = %v", err, tc.wantErr)
			}
			got := buf.String()
			for _, needle := range tc.wantInLine {
				if !strings.Contains(got, needle) {
					t.Errorf("output missing %q\nfull output: %q", needle, got)
				}
			}
			if !strings.HasSuffix(got, "\n") {
				t.Errorf("output does not end with newline: %q", got)
			}
		})
	}
}

func TestTextHandler_HandleHonoursCancelledContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"cancelled context returns ctx error without writing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			h := mustNewText(t, &buf, level.Debug)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			err := h.Handle(ctx, corelogger.RecordEvent{Level: level.Info, Message: "drop"})
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Handle(cancelled) err = %v, want context.Canceled", err)
			}
			if buf.Len() != 0 {
				t.Errorf("Handle(cancelled) wrote %d bytes, want 0", buf.Len())
			}
		})
	}
}

func TestTextHandler_WithAttrs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"child does not aliase parent attrs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			parent := mustNewText(t, &buf, level.Debug)
			parentWith := parent.WithAttrs([]corelogger.AttrValue{{Key: "p", Value: "P"}})
			childWith := parentWith.WithAttrs([]corelogger.AttrValue{{Key: "c", Value: "C"}})
			if err := childWith.Handle(t.Context(), corelogger.RecordEvent{Level: level.Info, Message: "m"}); err != nil {
				t.Fatalf("Handle err = %v", err)
			}
			line := buf.String()
			if !strings.Contains(line, `p="P"`) || !strings.Contains(line, `c="C"`) {
				t.Errorf("child output missing parent or child attrs: %q", line)
			}
			buf.Reset()
			if err := parentWith.Handle(t.Context(), corelogger.RecordEvent{Level: level.Info, Message: "m"}); err != nil {
				t.Fatalf("Handle err = %v", err)
			}
			if strings.Contains(buf.String(), `c="C"`) {
				t.Errorf("parent unexpectedly carries child attrs: %q", buf.String())
			}
		})
	}
}

// TestTextHandler_HandleIsConcurrentSafe launches short-lived goroutines via
// wg.Go; each worker returns after completing its writes, and wg.Wait joins
// them all before the test asserts on the aggregate output.
func TestTextHandler_HandleIsConcurrentSafe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		goroutines int
		perRoutine int
	}{
		{"10×50 concurrent writes produce 500 lines", 10, 50},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			h := mustNewText(t, &buf, level.Debug)
			var wg sync.WaitGroup
			for range tc.goroutines {
				wg.Go(func() {
					for range tc.perRoutine {
						if err := h.Handle(t.Context(), corelogger.RecordEvent{Level: level.Info, Message: "tick"}); err != nil {
							t.Errorf("Handle err = %v", err)
						}
					}
				})
			}
			wg.Wait()
			lines := strings.Count(buf.String(), "\n")
			want := tc.goroutines * tc.perRoutine
			if lines != want {
				t.Errorf("line count = %d, want %d", lines, want)
			}
		})
	}
}
