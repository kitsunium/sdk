package levelgate_test

import (
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/service/writer/levelgate"
)

// recordingSink counts the writes/flushes/closes it actually receives so the
// tests can assert which records passed the gate.
type recordingSink struct {
	writes  int
	flushes int
	closes  int
}

func (s *recordingSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	//: count the delivery and report the payload as fully written.
	s.writes++
	return len(p), nil
}
func (s *recordingSink) Flush(_ context.Context) error { s.flushes++; return nil }
func (s *recordingSink) Close() error                  { s.closes++; return nil }

func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		min      level.Level
		wantSame bool
	}
	tests := []tc{
		{"Info floor returns inner unwrapped", level.Info, true},
		{"non-Info floor wraps", level.Error, false},
		{"Debug floor wraps (below Info)", level.Debug, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		inner := &recordingSink{}
		got := levelgate.New(inner, c.min)
		//: identity comparison distinguishes the unwrapped fast path from a gate.
		if (got == corelogger.Sink(inner)) != c.wantSame {
			t.Errorf("%s: same=%v want %v", c.name, got == corelogger.Sink(inner), c.wantSame)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestGateWrite(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		min        level.Level
		recordLvl  level.Level
		wantPassed bool
	}
	tests := []tc{
		{"above floor passes", level.Error, level.Error, true},
		{"at floor passes", level.Warn, level.Warn, true},
		{"below floor drops", level.Error, level.Info, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		inner := &recordingSink{}
		gate := levelgate.New(inner, c.min)
		rec := corelogger.RecordEvent{Level: c.recordLvl}
		//: Write must always succeed (drops are a silent no-op success).
		if n, err := gate.Write(t.Context(), rec, []byte("xy")); err != nil || n != 2 {
			t.Fatalf("%s: Write=(%d,%v) want (2,nil)", c.name, n, err)
		}
		//: only at/above-floor records reach the wrapped sink.
		passed := inner.writes == 1
		if passed != c.wantPassed {
			t.Errorf("%s: passed=%v want %v", c.name, passed, c.wantPassed)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestGateFlushClose(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"flush and close forward to inner"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		inner := &recordingSink{}
		gate := levelgate.New(inner, level.Error)
		//: both lifecycle calls must reach the wrapped sink exactly once.
		if err := gate.Flush(t.Context()); err != nil || inner.flushes != 1 {
			t.Errorf("Flush: err=%v flushes=%d want nil,1", err, inner.flushes)
		}
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
