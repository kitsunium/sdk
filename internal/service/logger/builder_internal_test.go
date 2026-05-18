package logger

import (
	"bytes"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// compile-time assertion: chainBuilder must satisfy Builder so the pool's
// recycled pointer can flow through the interface without a runtime check.
// Kept in the test file per KTN-IFACE-ASSERT-PLACEMENT.
var _ Builder = (*chainBuilder)(nil)

// builderForTest returns a fresh chainBuilder bound to a discard-style
// loggerImpl backed by a captured bytes.Buffer.
func builderForTest(tb testing.TB) (*chainBuilder, *bytes.Buffer) {
	tb.Helper()
	var buf bytes.Buffer
	h := mustNewTextHandler(tb, &buf, level.Debug)
	cb := newChainBuilder()
	cb.owner = &loggerImpl{h: h}
	cb.lv = level.Info
	return cb, &buf
}

func Test_chainBuilder_Str(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"appends string attr"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cb, _ := builderForTest(t)
			cb.Str("k", "v")
			if len(cb.attrs) != 1 || cb.attrs[0].Value.String() != "v" {
				t.Errorf("Str did not append: %v", cb.attrs)
			}
		})
	}
}

func Test_chainBuilder_Int(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"appends int attr"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cb, _ := builderForTest(t)
			cb.Int("n", 7)
			if len(cb.attrs) != 1 || cb.attrs[0].Value.Int64() != 7 {
				t.Errorf("Int did not append: %v", cb.attrs)
			}
		})
	}
}

func Test_chainBuilder_Int64(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"appends int64 attr"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cb, _ := builderForTest(t)
			cb.Int64("big", 9001)
			if len(cb.attrs) != 1 || cb.attrs[0].Value.Int64() != 9001 {
				t.Errorf("Int64 did not append: %v", cb.attrs)
			}
		})
	}
}

func Test_chainBuilder_Uint64(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"appends uint64 attr"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cb, _ := builderForTest(t)
			cb.Uint64("u", 42)
			if len(cb.attrs) != 1 || cb.attrs[0].Value.Uint64() != 42 {
				t.Errorf("Uint64 did not append: %v", cb.attrs)
			}
		})
	}
}

func Test_chainBuilder_Bool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"appends bool attr"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cb, _ := builderForTest(t)
			cb.Bool("ok", true)
			if len(cb.attrs) != 1 || !cb.attrs[0].Value.Bool() {
				t.Errorf("Bool did not append: %v", cb.attrs)
			}
		})
	}
}

func Test_chainBuilder_Float64(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"appends float attr"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cb, _ := builderForTest(t)
			cb.Float64("r", 0.5)
			if len(cb.attrs) != 1 || cb.attrs[0].Value.Float64() != 0.5 {
				t.Errorf("Float64 did not append: %v", cb.attrs)
			}
		})
	}
}

func Test_chainBuilder_Duration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"appends duration attr"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cb, _ := builderForTest(t)
			cb.Duration("d", time.Second)
			if len(cb.attrs) != 1 || cb.attrs[0].Value.Duration() != time.Second {
				t.Errorf("Duration did not append: %v", cb.attrs)
			}
		})
	}
}

func Test_chainBuilder_Time(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"appends time attr"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cb, _ := builderForTest(t)
			now := time.Now()
			cb.Time("t", now)
			if len(cb.attrs) != 1 || !cb.attrs[0].Value.Time().Equal(now) {
				t.Errorf("Time did not append: %v", cb.attrs)
			}
		})
	}
}

func Test_chainBuilder_Any(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"appends any attr"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cb, _ := builderForTest(t)
			cb.Any("x", struct{}{})
			if len(cb.attrs) != 1 {
				t.Errorf("Any did not append: %v", cb.attrs)
			}
		})
	}
}

func Test_chainBuilder_Send(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Send emits the record and recycles the builder"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cb, buf := builderForTest(t)
			cb.Str("k", "v").Send(t.Context(), "msg")
			if buf.Len() == 0 {
				t.Error("Send did not emit anything")
			}
		})
	}
}

func Test_loggerImpl_Build(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Build returns a non-nil Builder"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			lg := &loggerImpl{h: mustNewTextHandler(t, &buf, level.Debug)}
			b := lg.Build(level.Info)
			if b == nil {
				t.Fatal("Build returned nil")
			}
			b.Str("k", "v").Send(t.Context(), "msg")
		})
	}
}

func Test_loggerImpl_LogAttrs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		min     level.Level
		lv      level.Level
		wantHit bool
	}{
		{"emit above min", level.Info, level.Warn, true},
		{"drop below min", level.Info, level.Debug, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			lg := &loggerImpl{h: mustNewTextHandler(t, &buf, tc.min)}
			lg.LogAttrs(t.Context(), tc.lv, "msg",
				[]corelogger.AttrValue{{Key: "k", Value: corelogger.StringValue("v")}})
			if (buf.Len() > 0) != tc.wantHit {
				t.Errorf("emit = %v, want %v", buf.Len() > 0, tc.wantHit)
			}
		})
	}
}

func Test_swallowHandlerError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{"nil error is a no-op", nil},
		{"non-nil error is silently dropped", errBoom{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: by contract this never panics or returns; we just ensure it runs.
			swallowHandlerError(tc.err)
		})
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
