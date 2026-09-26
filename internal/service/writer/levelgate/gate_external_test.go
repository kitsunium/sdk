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

// TestFloorAppliesEveryFloorInfoIncluded pins what separates Floor from New: at
// Info, New hands the sink back unwrapped — the writer configuration's
// "inherit" — so a Debug record would pass; Floor gates there like anywhere
// else. Reusing New for a caller's floor fails the first row.
func TestFloorAppliesEveryFloorInfoIncluded(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		min        level.Level
		recordLvl  level.Level
		wantPassed bool
	}
	tests := []tc{
		{"Info floor drops Debug", level.Info, level.Debug, false},
		{"Info floor passes Info", level.Info, level.Info, true},
		{"Warn floor drops Info", level.Warn, level.Info, false},
		{"Debug floor passes Debug", level.Debug, level.Debug, true},
		{"Error floor passes Error", level.Error, level.Error, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		inner := &recordingSink{}
		gate := levelgate.Floor(inner, c.min)
		//: a floor is always a gate, never the sink itself.
		if gate == corelogger.Sink(inner) {
			t.Fatalf("%s: Floor returned the sink unwrapped", c.name)
		}
		//: a drop is a successful no-op, like New's.
		if n, err := gate.Write(t.Context(), corelogger.RecordEvent{Level: c.recordLvl}, []byte("xyz")); err != nil || n != 3 {
			t.Fatalf("%s: Write=(%d,%v) want (3,nil)", c.name, n, err)
		}
		//: only at/above-floor records reach the wrapped sink.
		if passed := inner.writes == 1; passed != c.wantPassed {
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

// TestFloorOverNothingIsNothing pins the nil case: a gate over no sink would
// fail on its first record, so Floor answers nil, which a fan-out skips.
func TestFloorOverNothingIsNothing(t *testing.T) {
	t.Parallel()
	//: nil in, nil out — never a gate with nothing behind it.
	if got := levelgate.Floor(nil, level.Warn); got != nil {
		t.Fatalf("Floor(nil) = %T, want nil", got)
	}
}

// TestFloorDelegatesFlushAndClose pins that Floor's gate buffers and owns
// nothing: both lifecycle calls reach the wrapped sink once.
func TestFloorDelegatesFlushAndClose(t *testing.T) {
	t.Parallel()
	inner := &recordingSink{}
	gate := levelgate.Floor(inner, level.Info)
	//: forwarded verbatim.
	if err := gate.Flush(t.Context()); err != nil || inner.flushes != 1 {
		t.Errorf("Flush: err=%v flushes=%d want nil,1", err, inner.flushes)
	}
	//: forwarded verbatim.
	if err := gate.Close(); err != nil || inner.closes != 1 {
		t.Errorf("Close: err=%v closes=%d want nil,1", err, inner.closes)
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
