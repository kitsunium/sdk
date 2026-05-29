package levelgate

import (
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// countSink records how many writes/flushes/closes actually reached it.
type countSink struct {
	writes  int
	flushes int
	closes  int
}

func (s *countSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	//: count the delivery and accept the whole payload.
	s.writes++
	return len(p), nil
}
func (s *countSink) Flush(_ context.Context) error { s.flushes++; return nil }
func (s *countSink) Close() error                  { s.closes++; return nil }

func Test_gateSink_Write(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		min        level.Level
		recordLvl  level.Level
		wantPassed bool
	}
	tests := []tc{
		{"below floor drops", level.Error, level.Warn, false},
		{"at floor passes", level.Error, level.Error, true},
		{"above floor passes", level.Warn, level.Error, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		inner := &countSink{}
		//: white-box: build the gate directly to exercise the drop branch.
		gate := &gateSink{inner: inner, min: c.min}
		rec := corelogger.RecordEvent{Level: c.recordLvl}
		//: Write always succeeds; drops are a silent no-op.
		if n, err := gate.Write(t.Context(), rec, []byte("z")); err != nil || n != 1 {
			t.Fatalf("%s: Write=(%d,%v) want (1,nil)", c.name, n, err)
		}
		//: only at/above-floor records reach the wrapped sink.
		if (inner.writes == 1) != c.wantPassed {
			t.Errorf("%s: passed=%v want %v", c.name, inner.writes == 1, c.wantPassed)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_gateSink_Flush(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"flush forwards to inner"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		inner := &countSink{}
		gate := &gateSink{inner: inner, min: level.Error}
		//: the flush must reach the wrapped sink exactly once.
		if err := gate.Flush(t.Context()); err != nil || inner.flushes != 1 {
			t.Errorf("Flush: err=%v flushes=%d want nil,1", err, inner.flushes)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_gateSink_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"close forwards to inner"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		inner := &countSink{}
		gate := &gateSink{inner: inner, min: level.Error}
		//: the close must reach the wrapped sink exactly once.
		if err := gate.Close(); err != nil || inner.closes != 1 {
			t.Errorf("Close: err=%v closes=%d want nil,1", err, inner.closes)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
