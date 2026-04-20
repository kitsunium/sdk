package logger

import (
	"context"
	"testing"

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
		ctxDone bool
		want    bool
	}{
		{"above min is enabled", level.Info, level.Warn, false, true},
		{"at min is enabled", level.Info, level.Info, false, true},
		{"below min is disabled", level.Info, level.Debug, false, false},
		{"cancelled ctx disables", level.Debug, level.Error, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := mustNewGeneric(t, tc.min)
			ctx := t.Context()
			if tc.ctxDone {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			rec := corelogger.RecordEvent{Level: tc.query}
			if got := h.Enabled(ctx, rec); got != tc.want {
				t.Errorf("Enabled(%v, ctxDone=%v) = %v, want %v", tc.query, tc.ctxDone, got, tc.want)
			}
		})
	}
}

func Test_genericHandler_Handle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ctxDone bool
		wantErr bool
	}{
		{"happy path returns nil", false, false},
		{"cancelled ctx returns error", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := mustNewGeneric(t, level.Debug)
			ctx := t.Context()
			if tc.ctxDone {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			err := h.Handle(ctx, corelogger.RecordEvent{Level: level.Info, Message: "m"})
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
		{"empty parent + non-empty child clones", nil,
			[]corelogger.AttrValue{{Key: "k"}}, 1},
		{"non-empty parent + empty child concats", []corelogger.AttrValue{{Key: "p"}}, nil, 1},
		{"both non-empty concats parent first",
			[]corelogger.AttrValue{{Key: "p"}}, []corelogger.AttrValue{{Key: "c"}}, 2},
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
