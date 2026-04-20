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
