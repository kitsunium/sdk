package logger

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// failingWriter returns a canned error from Write so writeLine can exercise
// the WriteFailed wrapping branch.
type failingWriter struct{ err error }

func (f failingWriter) Write(_ []byte) (int, error) { return 0, f.err }

func Test_appendAttr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		attr corelogger.AttrValue
		want string
	}{
		{"string is quoted", corelogger.AttrValue{Key: "k", Value: "v"}, ` k="v"`},
		{"int renders decimal unquoted", corelogger.AttrValue{Key: "n", Value: 42}, " n=42"},
		{"int64 renders decimal unquoted", corelogger.AttrValue{Key: "big", Value: int64(9001)}, " big=9001"},
		{"bool renders literal", corelogger.AttrValue{Key: "ok", Value: true}, " ok=true"},
		{"float renders shortest round-trip", corelogger.AttrValue{Key: "r", Value: 0.25}, " r=0.25"},
		{"unknown value type marked with ?", corelogger.AttrValue{Key: "x", Value: []int{1, 2}}, " x=?"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := string(appendAttr(nil, tc.attr))
			if got != tc.want {
				t.Errorf("appendAttr(%#v) = %q, want %q", tc.attr, got, tc.want)
			}
		})
	}
}

func Test_constants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		got  int
		want int
	}{
		{"decimalBase is 10", decimalBase, 10},
		{"floatPrec is -1 (shortest round-trip)", floatPrec, -1},
		{"floatBitSize is 64", floatBitSize, 64},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Errorf("%s: got %d, want %d", tc.name, tc.got, tc.want)
			}
		})
	}
}

func TestTextHandler_renderLine(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		minLevel level.Level
		record   corelogger.RecordEvent
		wantSub  []string
	}
	tests := []tc{
		{
			name:     "includes level and message",
			minLevel: level.Debug,
			record:   corelogger.RecordEvent{Level: level.Info, Message: "hello"},
			wantSub:  []string{"INFO", "hello", "\n"},
		},
		{
			name:     "zero time gets clock fallback (non-empty timestamp prefix)",
			minLevel: level.Debug,
			record:   corelogger.RecordEvent{Level: level.Warn, Message: "timed"},
			wantSub:  []string{"WARN", "timed", "20"},
		},
		{
			name:     "record attrs rendered after message",
			minLevel: level.Debug,
			record: corelogger.RecordEvent{
				Level:   level.Error,
				Message: "attrs",
				Attrs:   []corelogger.AttrValue{{Key: "k", Value: "v"}},
			},
			wantSub: []string{"ERROR", "attrs", `k="v"`},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h, err := NewTextHandler(io.Discard, c.minLevel)
		if err != nil {
			t.Fatalf("NewTextHandler err = %v", err)
		}
		got := string(h.renderLine(nil, c.record))
		for _, needle := range c.wantSub {
			if !strings.Contains(got, needle) {
				t.Errorf("renderLine output missing %q: %q", needle, got)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestTextHandler_writeLine(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := []struct {
		name    string
		writer  io.Writer
		wantErr bool
		wantIs  error
	}{
		{"happy path", &bytes.Buffer{}, false, nil},
		{"writer error is wrapped", failingWriter{err: boom}, true, boom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, err := NewTextHandler(tc.writer, level.Debug)
			if err != nil {
				t.Fatalf("NewTextHandler err = %v", err)
			}
			got := h.writeLine([]byte("line\n"))
			if (got != nil) != tc.wantErr {
				t.Errorf("writeLine err = %v, wantErr = %v", got, tc.wantErr)
			}
			if tc.wantIs != nil && !errors.Is(got, tc.wantIs) {
				t.Errorf("errors.Is(err, boom) = false: %v", got)
			}
		})
	}
}
