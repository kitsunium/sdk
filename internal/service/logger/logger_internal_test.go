package logger

import (
	"bytes"
	"io"
	"strings"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// mustNewTextHandler builds a TextHandler or fails the test. Shared helper
// used by white-box tests that care about post-construction behaviour.
func mustNewTextHandler(tb testing.TB, w io.Writer, min level.Level) *TextHandler {
	tb.Helper()
	h, err := NewTextHandler(w, min)
	if err != nil {
		tb.Fatalf("NewTextHandler returned err: %v", err)
	}
	return h
}

func Test_loggerImpl_Enabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		min    level.Level
		query  level.Level
		wantEn bool
	}{
		{"query above min is enabled", level.Info, level.Warn, true},
		{"query at min is enabled", level.Info, level.Info, true},
		{"query below min is disabled", level.Info, level.Debug, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lg := &loggerImpl{h: mustNewTextHandler(t, &bytes.Buffer{}, tc.min)}
			if got := lg.Enabled(t.Context(), tc.query); got != tc.wantEn {
				t.Errorf("Enabled(%v) = %v, want %v", tc.query, got, tc.wantEn)
			}
		})
	}
}

func Test_loggerImpl_Log(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		min        level.Level
		callLevel  level.Level
		message    string
		wantOutput bool
	}{
		{"above min emits a line", level.Info, level.Warn, "hi", true},
		{"below min emits nothing", level.Info, level.Debug, "quiet", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			lg := &loggerImpl{h: mustNewTextHandler(t, &buf, tc.min)}
			lg.Log(t.Context(), tc.callLevel, tc.message)
			got := buf.Len() > 0
			if got != tc.wantOutput {
				t.Errorf("Log(%v, %q) output=%v, want %v (buf=%q)", tc.callLevel, tc.message, got, tc.wantOutput, buf.String())
			}
			if tc.wantOutput && !strings.Contains(buf.String(), tc.message) {
				t.Errorf("output missing message %q: %q", tc.message, buf.String())
			}
		})
	}
}

func Test_loggerImpl_With(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		attrs  []corelogger.AttrValue
		expect string
	}{
		{"binds a string attr", []corelogger.AttrValue{{Key: "k", Value: corelogger.StringValue("v")}}, `k="v"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			base := &loggerImpl{h: mustNewTextHandler(t, &buf, level.Debug)}
			child := base.With(tc.attrs...)
			if child == nil {
				t.Fatal("With returned nil")
				return
			}
			child.Log(t.Context(), level.Info, "m")
			if !strings.Contains(buf.String(), tc.expect) {
				t.Errorf("With attrs not emitted; got %q, want contains %q", buf.String(), tc.expect)
			}
		})
	}
}
